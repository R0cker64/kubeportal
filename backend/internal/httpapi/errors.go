package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Error     string `json:"error"`
	RequestID string `json:"requestId"`
}

// writeError sends a generic message plus the correlation id. Details belong
// in server logs only.
func writeError(w http.ResponseWriter, r *http.Request, status int) {
	writeJSON(w, status, errorBody{
		Error:     http.StatusText(status),
		RequestID: RequestID(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
