package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ellanetworks/smsc/internal/db"
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

func writeError(w http.ResponseWriter, r *http.Request, status int, message string, err error, logger *slog.Logger) {
	if status == http.StatusInternalServerError && errors.Is(err, db.ErrBusy) {
		w.Header().Set("Retry-After", "1")
		writeJSON(w, r, ErrorResponse{Error: "Too busy, retry later"}, http.StatusServiceUnavailable, logger)

		return
	}

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
