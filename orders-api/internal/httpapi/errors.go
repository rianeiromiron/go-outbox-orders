package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody es la forma única de todos los errores de la API.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpapi: escribir respuesta", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string, details ...string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message, Details: details}})
}
