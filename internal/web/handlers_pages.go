package web

import (
	"io/fs"
	"net/http"
)

// handleIndex serves the SPA shell (static/index.html). Every browser-visible
// route in routes.go aliases here; HTMX swaps fragments into #main based on
// the current location.pathname. We set the CSRF cookie before responding so
// the first XHR call carries it.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	_ = s.csrf.ensureCookie(w, r)
	sub, err := readEmbedFS()
	if err != nil {
		http.Error(w, "asset load: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "missing index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleFavicon returns the embedded logo as the favicon. Browsers hammer
// /favicon.ico on every page load; serving a real response keeps the access
// log clean and avoids a 404.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	sub, err := readEmbedFS()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(sub, "img/logo.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}
