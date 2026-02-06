package ui

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/bvk/tradebot/httputil"
)

//go:embed static/*
var staticFS embed.FS

var (
	indexTmpl = template.Must(template.ParseFS(staticFS, "static/index.html"))
)

// RegisterHandlers registers HTTP handlers that serve the HTML UI and static
// assets. It is designed to be called from the main daemon after its HTTP
// server is created, so the UI is hosted alongside existing APIs.
func RegisterHandlers(s *httputil.Server) {
	// Home page.
	s.AddHandler("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		if err := indexTmpl.Execute(w, nil); err != nil {
			http.Error(w, fmt.Sprintf("could not render template: %v", err), http.StatusInternalServerError)
			return
		}
	}))

	// Static assets (JS, CSS, images) under /static/.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// If this fails at startup, make it very obvious.
		panic(fmt.Errorf("ui: could not create static sub filesystem: %w", err))
	}
	fileServer := http.FileServer(http.FS(staticSub))
	s.AddHandler("/static/", http.StripPrefix("/static/", fileServer))
}
