package testui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/testui"
)

type response struct {
	status int
	header http.Header
	body   string
}

// fetch llama al handler directamente y devuelve la respuesta ya leída (con el
// ResponseRecorder no hay cuerpo que cerrar).
func fetch(t *testing.T, path string) response {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	testui.Handler().ServeHTTP(rec, req)
	return response{status: rec.Code, header: rec.Header(), body: rec.Body.String()}
}

func TestServesIndexWithCorrectTypes(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{"/ui/", "text/html; charset=utf-8", "página de prueba"},
		{"/ui/app.js", "text/javascript; charset=utf-8", "'use strict'"},
		{"/ui/style.css", "text/css; charset=utf-8", "--accent"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			res := fetch(t, tt.path)
			if res.status != http.StatusOK {
				t.Fatalf("status = %d; quería 200", res.status)
			}
			if got := res.header.Get("Content-Type"); got != tt.wantType {
				t.Errorf("Content-Type = %q; quería %q", got, tt.wantType)
			}
			if !strings.Contains(res.body, tt.wantBody) {
				t.Errorf("el cuerpo no contiene %q", tt.wantBody)
			}
		})
	}
}

// Todas las respuestas (incluidos los 404) llevan las cabeceras de seguridad.
func TestSecurityHeaders(t *testing.T) {
	for _, path := range []string{"/ui/", "/ui/app.js", "/ui/style.css", "/ui/no-existe"} {
		t.Run(path, func(t *testing.T) {
			res := fetch(t, path)

			csp := res.header.Get("Content-Security-Policy")
			for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
				if !strings.Contains(csp, want) {
					t.Errorf("CSP = %q; falta %q", csp, want)
				}
			}
			if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
				t.Errorf("CSP no debe permitir scripts o estilos en línea ni eval: %q", csp)
			}
			if got := res.header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q; quería nosniff", got)
			}
			if got := res.header.Get("X-Frame-Options"); got != "DENY" {
				t.Errorf("X-Frame-Options = %q; quería DENY", got)
			}
		})
	}
}

func TestUnknownFilesAndTraversalAreNotServed(t *testing.T) {
	for _, path := range []string{"/ui/no-existe", "/ui/../go.mod", "/ui/%2e%2e/go.mod", "/ui/static/app.js", "/ui/testui.go"} {
		t.Run(path, func(t *testing.T) {
			if res := fetch(t, path); res.status == http.StatusOK {
				t.Errorf("GET %s = 200; no debía servirse", path)
			}
		})
	}
}

func TestIndexDoesNotListDirectory(t *testing.T) {
	// /ui/ sirve index.html, no un listado de archivos.
	if b := fetch(t, "/ui/").body; strings.Contains(b, "<pre>") && strings.Contains(b, "app.js</a>") {
		t.Error("/ui/ parece devolver un listado de directorio")
	}
}

// La CSP prohíbe lo que sigue; este test lo detecta antes de que el navegador
// lo bloquee en silencio.
func TestHTMLHasNoInlineCode(t *testing.T) {
	html := fetch(t, "/ui/").body

	scripts := regexp.MustCompile(`(?is)<script[^>]*>`).FindAllString(html, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html no carga ningún script")
	}
	for _, tag := range scripts {
		if !regexp.MustCompile(`(?i)\bsrc\s*=`).MatchString(tag) {
			t.Errorf("script en línea (sin src): %s", tag)
		}
	}
	if m := regexp.MustCompile(`(?i)\son[a-z]+\s*=`).FindString(html); m != "" {
		t.Errorf("manejador de evento en línea: %q", m)
	}
	if m := regexp.MustCompile(`(?i)\sstyle\s*=`).FindString(html); m != "" {
		t.Errorf("atributo style en línea: %q", m)
	}
	if strings.Contains(strings.ToLower(html), "<style") {
		t.Error("bloque <style> en línea")
	}
	if m := regexp.MustCompile(`(?i)(?:src|href)\s*=\s*"(?:https?:)?//`).FindString(html); m != "" {
		t.Errorf("recurso externo (la página no debe depender de CDNs): %q", m)
	}
}

// La API devuelve datos escritos por el usuario (p. ej. el email): nada de
// insertarlos como HTML.
func TestJSNeverInjectsHTML(t *testing.T) {
	js := fetch(t, "/ui/app.js").body
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
		if strings.Contains(js, banned) {
			t.Errorf("app.js usa %q; el texto dinámico debe entrar con textContent", banned)
		}
	}
}

// Cada $('id') del JS debe existir en el HTML; sin esto un typo solo se ve al
// abrir la página y hacer clic.
func TestEveryIDUsedByJSExistsInHTML(t *testing.T) {
	html := fetch(t, "/ui/").body
	js := fetch(t, "/ui/app.js").body

	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}

	used := regexp.MustCompile(`\$\('([a-z0-9-]+)'\)`).FindAllStringSubmatch(js, -1)
	if len(used) == 0 {
		t.Fatal("no se encontró ningún $('id') en app.js; ¿cambió la convención?")
	}
	for _, m := range used {
		if !ids[m[1]] {
			t.Errorf("app.js usa $('%s') pero index.html no tiene id=%q", m[1], m[1])
		}
	}

	// Los botones "Copiar" apuntan por data-copy a un elemento que debe existir.
	for _, m := range regexp.MustCompile(`data-copy="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		if !ids[m[1]] {
			t.Errorf("data-copy=%q no apunta a ningún id", m[1])
		}
	}
}
