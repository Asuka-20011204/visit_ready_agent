package webui

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed index.html assets/*
var content embed.FS

type handler struct {
	api       http.Handler
	index     *template.Template
	assets    http.Handler
	mode      string
	retention string
}

func New(api http.Handler, mode string, sessionTTL ...time.Duration) (http.Handler, error) {
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
	ttl := 24 * time.Hour
	if len(sessionTTL) > 0 && sessionTTL[0] > 0 {
		ttl = sessionTTL[0]
	}
	return &handler{api: api, index: index, assets: http.FileServer(http.FS(assetFS)), mode: mode, retention: formatDuration(ttl)}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" || r.URL.Path == "/livez" || r.URL.Path == "/readyz" || len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
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
		if err := h.index.Execute(w, map[string]any{"Mode": h.mode, "Demo": h.mode == "demo", "Retention": h.retention}); err != nil {
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

func formatDuration(value time.Duration) string {
	if value%time.Hour == 0 {
		hours := int(value / time.Hour)
		if hours%24 == 0 {
			return fmt.Sprintf("%d 天", hours/24)
		}
		return fmt.Sprintf("%d 小时", hours)
	}
	if value >= time.Hour {
		return fmt.Sprintf("%d 小时 %d 分钟", int(value/time.Hour), int(value%time.Hour/time.Minute))
	}
	minutes := max(1, int(value/time.Minute))
	return fmt.Sprintf("%d 分钟", minutes)
}

func setHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self'; object-src 'none'; script-src 'self'; style-src 'self'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
