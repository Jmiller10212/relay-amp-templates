package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func NoStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, status int, code, message, field string) {
	NoStore(w)
	WriteJSON(w, status, map[string]any{"error": Error{Code: code, Message: message, Field: field}})
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		WriteError(w, http.StatusUnsupportedMediaType, "json_required", "Content-Type must be application/json.", "")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.", "")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must contain one JSON object.", "")
		return false
	}
	return true
}
