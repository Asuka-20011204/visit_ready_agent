package webui

import (
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed index.html assets/*
var content embed.FS

type handler struct {
	api    http.Handler
	index  *template.Template
	assets http.Handler
	mode   string
}

func New(api http.Handler, mode string) (http.Handler, error) {
	if api == nil {
		return nil, errors.New("API handler is required")
	}
	index, err := template.ParseFS(content, "index.html")
	if err != nil {
		return nil, err
	}
	assetFS, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, err
	}
	return &handler{api: api, index: index, assets: http.FileServer(http.FS(assetFS)), mode: mode}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
		h.api.ServeHTTP(w, r)
		return
	}
	setHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/" {
		w.Header().Set("Cache-Control", "no-store")
		if err := h.index.Execute(w, map[string]any{"Mode": h.mode, "Demo": h.mode == "demo"}); err != nil {
			http.Error(w, "render page", http.StatusInternalServerError)
		}
		return
	}
	if len(r.URL.Path) >= 8 && r.URL.Path[:8] == "/assets/" {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		r.URL.Path = r.URL.Path[8:]
		h.assets.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func setHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self'; object-src 'none'; script-src 'self'; style-src 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
