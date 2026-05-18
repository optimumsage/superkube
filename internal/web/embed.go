package web

import "embed"

// staticFS holds every browser-facing asset: the HTML shell, hand-rolled CSS,
// vendored JS libraries (HTMX, Alpine, xterm), and html/template fragments.
// Templates live under static/templates/ and are parsed at startup; everything
// else is served directly via http.FileServer.

//go:embed static/index.html static/css/* static/js/* static/img/* static/templates/*
var staticFS embed.FS
