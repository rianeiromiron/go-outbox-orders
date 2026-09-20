package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/httpapi"
)

// Estos tests no necesitan base de datos: las rutas de la UI y /healthz no usan
// el servicio de pedidos.
func uiRouter(opts ...httpapi.Option) http.Handler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ping := func(context.Context) error { return nil }
	return httpapi.NewRouter(nil, ping, log, opts...)
}

func status(h http.Handler, path string) (int, string) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

// Por defecto la página NO se expone: una API "de producción" no debe traer una
// UI de pruebas sin pedirla.
func TestTestUIIsOffByDefault(t *testing.T) {
	h := uiRouter()

	for _, path := range []string{"/", "/ui", "/ui/", "/ui/app.js"} {
		if code, _ := status(h, path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d sin WithTestUI; quería 404", path, code)
		}
	}
	if code, _ := status(h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d; la API debe seguir funcionando", code)
	}
}

func TestTestUIWhenEnabled(t *testing.T) {
	h := uiRouter(httpapi.WithTestUI())

	for _, path := range []string{"/", "/ui"} {
		code, loc := status(h, path)
		if code != http.StatusFound || loc != "/ui/" {
			t.Errorf("GET %s = %d Location=%q; quería 302 a /ui/", path, code, loc)
		}
	}
	for _, path := range []string{"/ui/", "/ui/app.js", "/ui/style.css"} {
		if code, _ := status(h, path); code != http.StatusOK {
			t.Errorf("GET %s = %d; quería 200", path, code)
		}
	}
	// Activar la UI no debe tapar ni alterar las rutas de la API.
	if code, _ := status(h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d con la UI activa; quería 200", code)
	}
	if code, _ := status(h, "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d con la UI activa; quería 200", code)
	}
}
