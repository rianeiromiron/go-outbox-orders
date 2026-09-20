// Package testui sirve una página HTML de prueba para orders-api. Está pensada
// solo para desarrollo local: el binario la trae embebida, pero el router solo la
// expone si se pide expresamente (ENABLE_TEST_UI=true).
//
// Se sirve desde la propia API, así que es del mismo origen y no hace falta CORS.
package testui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed static
var content embed.FS

// Prefix es la ruta bajo la que se monta la página.
const Prefix = "/ui/"

// csp deja cargar solo recursos del propio origen y prohíbe scripts y estilos en
// línea, iframes y cualquier conexión a otro origen. La página no necesita más.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// contentTypes fija el tipo de cada archivo. Se hace a mano porque mime.TypeByExtension
// en Windows consulta el registro y puede devolver text/plain para .js, y con
// X-Content-Type-Options: nosniff el navegador se negaría a ejecutarlo.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// Handler devuelve el handler de la página. Debe montarse bajo Prefix.
func Handler() http.Handler {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic("testui: el directorio embebido no existe: " + err.Error()) // error de programación
	}
	files := http.StripPrefix(Prefix, http.FileServerFS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", csp)
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-cache")

		name := path.Base(r.URL.Path)
		if name == "." || name == "/" || name == "ui" {
			name = "index.html" // la raíz de /ui/ sirve index.html
		}
		if ct, ok := contentTypes[path.Ext(name)]; ok {
			hd.Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
}
