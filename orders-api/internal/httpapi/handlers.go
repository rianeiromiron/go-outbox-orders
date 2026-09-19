package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/rianeiromiron/go-outbox-orders/orders-api/internal/order"
)

const maxBodyBytes = 1 << 20 // 1 MiB

type handlers struct {
	orders *order.Service
	ping   func(context.Context) error
	log    *slog.Logger
}

func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request) {
	var in order.CreateInput
	if !h.decodeJSON(w, r, &in) {
		return
	}

	o, err := h.orders.Create(r.Context(), in)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.Header().Set("Location", "/orders/"+o.ID.String())
	writeJSON(w, http.StatusCreated, o)
}

func (h *handlers) getOrder(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "el id del pedido debe ser un UUID")
		return
	}

	o, err := h.orders.Get(r.Context(), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (h *handlers) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handlers) readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.ping(r.Context()); err != nil {
		h.log.Warn("readyz: base de datos no disponible", "err", err)
		writeError(w, http.StatusServiceUnavailable, "not_ready", "la base de datos no responde")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// writeServiceError traduce errores del dominio a respuestas HTTP. Lo que no
// se reconoce es un 500 genérico: el detalle va al log, nunca al cliente.
func (h *handlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *order.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "la petición no es válida", verr.Details...)
	case errors.Is(err, order.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "pedido no encontrado")
	default:
		h.log.Error("error interno", "err", err, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusInternalServerError, "internal_error", "error interno")
	}
}

// decodeJSON lee un único objeto JSON estricto (sin campos desconocidos ni
// datos sobrantes). Devuelve false si ya respondió con el error.
func (h *handlers) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil {
		if _, extra := dec.Token(); !errors.Is(extra, io.EOF) {
			err = errors.New("el cuerpo debe contener un solo objeto JSON")
		}
	}
	if err == nil {
		return true
	}

	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "el cuerpo excede el tamaño máximo")
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido: "+err.Error())
	return false
}
