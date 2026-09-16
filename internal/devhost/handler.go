package devhost

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
)

const apiVersion = "aspm/v1alpha1"

func New(assets fs.FS) (http.Handler, error) {
	info, err := fs.Stat(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("built index.html is unavailable; run npm run build in web: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("built index.html must be a file")
	}
	var sequence atomic.Uint64
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusServiceUnavailable
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			status = http.StatusMethodNotAllowed
			w.Header().Set("Allow", "GET, HEAD")
		}
		writeJSON(w, status, map[string]any{
			"apiVersion": apiVersion,
			"error": map[string]any{
				"code":      "unavailable",
				"message":   "The M01 data API is not implemented. This development host serves the interface only.",
				"requestId": fmt.Sprintf("dev-%016x", sequence.Add(1)),
				"retryable": false,
			},
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"apiVersion": apiVersion, "status": "development-host-only",
			"dataAPIs": "unavailable", "authentication": "not-implemented",
		})
	})
	mux.HandleFunc("/api/", unavailable)
	mux.HandleFunc("/api", unavailable)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Read-only development host.", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if r.URL.Path == "/" {
			name = "index.html"
		}
		if !fs.ValidPath(name) || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFileFS(w, r, assets, name)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	}), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("write development response", "error", err)
	}
}
