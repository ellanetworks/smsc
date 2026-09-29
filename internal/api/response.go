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

func writeResponse(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	writeJSON(w, Response{Result: v}, status, logger)
}

func writeError(w http.ResponseWriter, status int, message string, err error, logger *slog.Logger) {
	if status >= 500 {
		logger.Error(message, slog.Any("error", err))
	} else {
		logger.Debug(message, slog.Any("error", err))
	}

	writeJSON(w, ErrorResponse{Error: message}, status, logger)
}

func writeJSON(w http.ResponseWriter, v any, status int, logger *slog.Logger) {
	b, err := json.Marshal(v)
	if err != nil {
		logger.Error("failed to encode API response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(b); err != nil {
		logger.Debug("failed to write API response", slog.Any("error", err))
	}
}
