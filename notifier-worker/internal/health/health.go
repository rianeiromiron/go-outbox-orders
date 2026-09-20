// Package health expone las sondas HTTP del worker (que no tiene API propia):
// /healthz (liveness) y /readyz (readiness) para Docker y Kubernetes.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

// Checker comprueba una dependencia. Debe respetar el contexto (timeout).
type Checker func(ctx context.Context) error

// Response es el cuerpo JSON de las sondas.
type Response struct {
	Status string   `json:"status"`
	Failed []string `json:"failed,omitempty"`
}

// NewHandler devuelve el handler con:
//
//	GET /healthz  liveness: 200 mientras el proceso responda. No consulta
//	              dependencias a propósito: si Redis o Postgres caen, reiniciar
//	              el worker no las arregla y solo provocaría reinicios en cadena.
//	GET /readyz   readiness: 200 solo si TODAS las comprobaciones pasan; si no,
//	              503 con los NOMBRES de las que fallan (el error va al log,
//	              nunca al cliente).
//
// Cada comprobación tiene como máximo `timeout` para responder.
func NewHandler(log *slog.Logger, checks map[string]Checker, timeout time.Duration) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Response{Status: "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		var failed []string
		for name, check := range checks {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			err := check(ctx)
			cancel()
			if err != nil {
				log.Warn("readyz: comprobación fallida", "check", name, "err", err)
				failed = append(failed, name)
			}
		}
		if len(failed) > 0 {
			sort.Strings(failed)
			writeJSON(w, http.StatusServiceUnavailable, Response{Status: "not_ready", Failed: failed})
			return
		}
		writeJSON(w, http.StatusOK, Response{Status: "ready"})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
