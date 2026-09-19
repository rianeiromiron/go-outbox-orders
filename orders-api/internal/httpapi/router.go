// Package httpapi expone el servicio de pedidos por HTTP.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
)

const requestTimeout = 10 * time.Second

// NewRouter arma el router. ping se usa en /readyz para verificar la base de datos.
func NewRouter(orders *order.Service, ping func(context.Context) error, log *slog.Logger) http.Handler {
	h := &handlers{orders: orders, ping: ping, log: log}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)

	// Las probes van fuera del grupo con timeout: son baratas y readyz ya
	// acota su propia consulta con el contexto de la petición.
	r.Get("/healthz", h.healthz)
	r.Get("/readyz", h.readyz)

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(requestTimeout))
		r.Post("/orders", h.createOrder)
		r.Get("/orders/{id}", h.getOrder)
	})
	return r
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
