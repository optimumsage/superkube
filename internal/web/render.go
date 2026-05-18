package web

import (
	"encoding/json"
	"html/template"
	"net/http"
)

// renderFns bundles the helpers handlers use to write responses. Centralizing
// the JSON-vs-template branch keeps individual handler files short.
type renderFns struct {
	tmpls *template.Template
}

func newRenderFns(t *template.Template) renderFns {
	return renderFns{tmpls: t}
}

// JSON writes a JSON-encoded body with the given status code. Errors during
// marshal/write are silenced — by the time we'd discover them the response
// has typically already been partially written.
func (r renderFns) JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// Error writes a JSON error response with the given status.
func (r renderFns) Error(w http.ResponseWriter, status int, msg string) {
	r.JSON(w, status, map[string]string{"error": msg})
}

// Template renders a named template (the page's filename without extension or
// with — both are registered) into w. Returns the underlying ExecuteTemplate
// error so callers can choose how loud to be.
func (r renderFns) Template(w http.ResponseWriter, name string, data any) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return r.tmpls.ExecuteTemplate(w, name, data)
}

// HTML writes a literal HTML string. Used by handlers that compose tiny
// fragments inline (e.g. HTMX swaps with a single toast).
func (r renderFns) HTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
