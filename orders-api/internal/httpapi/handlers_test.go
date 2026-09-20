package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/httpapi"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/outbox"
	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/testdb"
)

const validBody = `{
	"customer_email": "ana@example.com",
	"currency": "USD",
	"items": [
		{"sku": "SKU-1", "quantity": 2, "unit_price_cents": 1500},
		{"sku": "SKU-2", "quantity": 1, "unit_price_cents": 999}
	]
}`

func newRouter(t *testing.T, ping func(context.Context) error) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	db := testdb.Pool(t)
	if ping == nil {
		ping = db.Ping
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpapi.NewRouter(order.NewService(db, outbox.Store{}), ping, log), db
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type errResponse struct {
	Error struct {
		Code    string   `json:"code"`
		Message string   `json:"message"`
		Details []string `json:"details"`
	} `json:"error"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errResponse {
	t.Helper()
	var e errResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("el error no es JSON con la forma esperada: %v\ncuerpo: %s", err, rec.Body)
	}
	return e
}

func TestPostOrders_Created(t *testing.T) {
	h, db := newRouter(t, nil)

	rec := do(h, http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; quería 201\ncuerpo: %s", rec.Code, rec.Body)
	}
	var o order.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if o.TotalCents != 3999 || o.Status != "pending" || len(o.Items) != 2 {
		t.Errorf("respuesta = %+v", o)
	}
	if loc := rec.Header().Get("Location"); loc != "/orders/"+o.ID.String() {
		t.Errorf("Location = %q", loc)
	}
	if n := testdb.Count(t, db, "orders"); n != 1 {
		t.Errorf("orders = %d; quería 1", n)
	}
	if n := testdb.Count(t, db, "outbox_events"); n != 1 {
		t.Errorf("outbox_events = %d; quería 1", n)
	}
}

// Desde la API: si falla la inserción del evento outbox, responde 500 sin
// filtrar el error interno, y no queda nada guardado.
func TestPostOrders_OutboxFailureRollsBackAndHidesInternals(t *testing.T) {
	h, db := newRouter(t, nil)
	removeTrigger := testdb.FailInserts(t, db, "outbox_events")

	rec := do(h, http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; quería 500", rec.Code)
	}
	if e := decodeError(t, rec); e.Error.Code != "internal_error" {
		t.Errorf("code = %q; quería internal_error", e.Error.Code)
	}
	if strings.Contains(rec.Body.String(), "fallo simulado") {
		t.Errorf("el 500 filtra detalles internos: %s", rec.Body)
	}
	for _, table := range []string{"orders", "order_items", "outbox_events"} {
		if n := testdb.Count(t, db, table); n != 0 {
			t.Errorf("%s = %d; quería 0 tras el rollback", table, n)
		}
	}

	// El cliente reintenta una vez resuelta la causa: exactamente uno de cada uno.
	removeTrigger()
	if rec := do(h, http.MethodPost, "/orders", validBody); rec.Code != http.StatusCreated {
		t.Fatalf("reintento: status = %d\ncuerpo: %s", rec.Code, rec.Body)
	}
	if o, e := testdb.Count(t, db, "orders"), testdb.Count(t, db, "outbox_events"); o != 1 || e != 1 {
		t.Errorf("tras el reintento orders=%d outbox_events=%d; quería 1 y 1", o, e)
	}
}

func TestPostOrders_BadRequests(t *testing.T) {
	h, db := newRouter(t, nil)

	tests := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"validación de dominio", `{"customer_email":"x","currency":"USD","items":[]}`, 422, "validation_error"},
		{"JSON malformado", `{"customer_email":`, 400, "invalid_json"},
		{"campo desconocido", `{"customer_email":"a@b.co","currency":"USD","total_cents":1,"items":[{"sku":"s","quantity":1,"unit_price_cents":1}]}`, 400, "invalid_json"},
		{"datos sobrantes", validBody + `{}`, 400, "invalid_json"},
		{"cuerpo vacío", ``, 400, "invalid_json"},
		{"cuerpo gigante", `{"customer_email":"` + strings.Repeat("a", 2<<20) + `"}`, 413, "body_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, "/orders", tt.body)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d; quería %d\ncuerpo: %.200s", rec.Code, tt.wantCode, rec.Body)
			}
			if e := decodeError(t, rec); e.Error.Code != tt.wantErr {
				t.Errorf("code = %q; quería %q", e.Error.Code, tt.wantErr)
			}
		})
	}
	if n := testdb.Count(t, db, "orders") + testdb.Count(t, db, "outbox_events"); n != 0 {
		t.Errorf("las peticiones inválidas escribieron %d filas", n)
	}
}

func TestPostOrders_ValidationErrorListsDetails(t *testing.T) {
	h, _ := newRouter(t, nil)

	rec := do(h, http.MethodPost, "/orders", `{"customer_email":"x","currency":"u","items":[]}`)

	if e := decodeError(t, rec); len(e.Error.Details) != 3 {
		t.Errorf("details = %v; quería 3", e.Error.Details)
	}
}

func TestGetOrder(t *testing.T) {
	h, _ := newRouter(t, nil)

	var created order.Order
	rec := do(h, http.MethodPost, "/orders", validBody)
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	t.Run("existente", func(t *testing.T) {
		rec := do(h, http.MethodGet, "/orders/"+created.ID.String(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; quería 200", rec.Code)
		}
		var got order.Order
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != created.ID || got.TotalCents != created.TotalCents || len(got.Items) != 2 {
			t.Errorf("got = %+v; created = %+v", got, created)
		}
		// Mismo formato exacto (UTC) en POST y GET, no solo el mismo instante.
		var raw struct {
			CreatedAt string `json:"created_at"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		if want := created.CreatedAt.Format(time.RFC3339Nano); raw.CreatedAt != want {
			t.Errorf("created_at en GET = %q; quería %q (UTC, como en el POST)", raw.CreatedAt, want)
		}
	})
	t.Run("inexistente", func(t *testing.T) {
		rec := do(h, http.MethodGet, "/orders/"+uuid.NewString(), "")
		if rec.Code != http.StatusNotFound || decodeError(t, rec).Error.Code != "not_found" {
			t.Errorf("status = %d cuerpo = %s; quería 404 not_found", rec.Code, rec.Body)
		}
	})
	t.Run("id inválido", func(t *testing.T) {
		rec := do(h, http.MethodGet, "/orders/abc", "")
		if rec.Code != http.StatusBadRequest || decodeError(t, rec).Error.Code != "invalid_id" {
			t.Errorf("status = %d cuerpo = %s; quería 400 invalid_id", rec.Code, rec.Body)
		}
	})
}

func TestHealthAndReadiness(t *testing.T) {
	t.Run("healthz", func(t *testing.T) {
		h, _ := newRouter(t, nil)
		if rec := do(h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
			t.Errorf("healthz = %d; quería 200", rec.Code)
		}
	})
	t.Run("readyz con BD", func(t *testing.T) {
		h, _ := newRouter(t, nil)
		if rec := do(h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusOK {
			t.Errorf("readyz = %d; quería 200", rec.Code)
		}
	})
	t.Run("readyz sin BD", func(t *testing.T) {
		h, _ := newRouter(t, func(context.Context) error { return errors.New("sin conexión") })
		rec := do(h, http.MethodGet, "/readyz", "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("readyz = %d; quería 503", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "sin conexión") {
			t.Errorf("readyz filtra el error interno: %s", rec.Body)
		}
	})
}
