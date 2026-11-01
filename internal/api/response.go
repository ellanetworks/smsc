package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type Response struct {
	Result any `json:"result,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func writeResponse(w http.ResponseWriter, r *http.Request, v any, status int, logger *slog.Logger) {
	writeJSON(w, r, Response{Result: v}, status, logger)
}

// writeError answers a request with an error, which it logs with the request's context, and so its span's trace
// context: at level error for a failure of the SMSC, debug for one of the client.
func writeError(w http.ResponseWriter, r *http.Request, status int, message string, err error, logger *slog.Logger) {
	if status >= 500 {
		logger.ErrorContext(r.Context(), message, slog.Any("error", err))
	} else {
		logger.DebugContext(r.Context(), message, slog.Any("error", err))
	}

	writeJSON(w, r, ErrorResponse{Error: message}, status, logger)
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.ErrorContext(r.Context(), "failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.DebugContext(r.Context(), "failed to write API response", slog.Any("error", err))
	}
}
