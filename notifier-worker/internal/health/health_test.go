package health_test

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

	"github.com/rianeiromiron/go-outbox-orders/notifier-worker/internal/health"
)

func get(t *testing.T, h http.Handler, method, path string) (int, health.Response) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var body health.Response
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("cuerpo no es JSON: %v\n%s", err, rec.Body)
		}
	}
	return rec.Code, body
}

func newHandler(checks map[string]health.Checker) http.Handler {
	return health.NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), checks, 50*time.Millisecond)
}

func ok(context.Context) error { return nil }

// La liveness no depende de nada: aunque todas las dependencias fallen sigue en
// 200, para que Kubernetes no reinicie el worker por un problema externo.
func TestHealthz_IgnoresDependencies(t *testing.T) {
	h := newHandler(map[string]health.Checker{
		"postgres": func(context.Context) error { return errors.New("caído") },
	})

	code, body := get(t, h, http.MethodGet, "/healthz")

	if code != http.StatusOK || body.Status != "ok" {
		t.Errorf("healthz = %d %+v; quería 200 ok", code, body)
	}
}

func TestReadyz_AllChecksPass(t *testing.T) {
	h := newHandler(map[string]health.Checker{"postgres": ok, "redis": ok})

	code, body := get(t, h, http.MethodGet, "/readyz")

	if code != http.StatusOK || body.Status != "ready" || len(body.Failed) != 0 {
		t.Errorf("readyz = %d %+v; quería 200 ready", code, body)
	}
}

func TestReadyz_ReportsWhichChecksFailWithoutLeakingErrors(t *testing.T) {
	h := newHandler(map[string]health.Checker{
		"postgres": ok,
		"redis":    func(context.Context) error { return errors.New("dial tcp 10.0.0.5:6379: secreto-interno") },
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; quería 503", rec.Code)
	}
	var body health.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || len(body.Failed) != 1 || body.Failed[0] != "redis" {
		t.Errorf("cuerpo = %+v; quería not_ready con failed=[redis]", body)
	}
	if strings.Contains(rec.Body.String(), "secreto-interno") || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("readyz filtra detalles internos: %s", rec.Body)
	}
}

func TestReadyz_ListsAllFailuresSorted(t *testing.T) {
	fail := func(context.Context) error { return errors.New("x") }
	h := newHandler(map[string]health.Checker{"redis": fail, "postgres": fail})

	_, body := get(t, h, http.MethodGet, "/readyz")

	if len(body.Failed) != 2 || body.Failed[0] != "postgres" || body.Failed[1] != "redis" {
		t.Errorf("failed = %v; quería [postgres redis] (ordenado, determinista)", body.Failed)
	}
}

// Una dependencia colgada no cuelga la sonda: el timeout la marca como fallida.
func TestReadyz_SlowCheckTimesOut(t *testing.T) {
	h := newHandler(map[string]health.Checker{
		"redis": func(ctx context.Context) error {
			<-ctx.Done() // se bloquea hasta que venza el timeout
			return ctx.Err()
		},
	})

	start := time.Now()
	code, body := get(t, h, http.MethodGet, "/readyz")

	if code != http.StatusServiceUnavailable || len(body.Failed) != 1 {
		t.Errorf("readyz = %d %+v; quería 503 por timeout", code, body)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("la sonda tardó %v; el timeout era 50 ms", elapsed)
	}
}

func TestOnlyGetIsAllowed(t *testing.T) {
	h := newHandler(map[string]health.Checker{"postgres": ok})

	for _, path := range []string{"/healthz", "/readyz"} {
		code, _ := get(t, h, http.MethodPost, path)
		if code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d; quería 405", path, code)
		}
	}
	if code, _ := get(t, h, http.MethodGet, "/otra-cosa"); code != http.StatusNotFound {
		t.Errorf("GET /otra-cosa = %d; quería 404", code)
	}
}
