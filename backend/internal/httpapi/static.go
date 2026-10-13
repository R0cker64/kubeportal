package httpapi

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spaHandler serves the Angular build. Unknown paths without a file extension
// fall back to index.html so client-side routes work on reload.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		// Directories are never listed: they fall back to the SPA like any
		// unknown route.
		if info, err := fs.Stat(static, name); err != nil || info.IsDir() {
			if path.Ext(name) != "" {
				writeError(w, r, http.StatusNotFound)
				return
			}
			name = "index.html"
		}
		if name == "index.html" {
			serveIndex(w, r, static)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// noncePlaceholder is set in frontend/src/index.html as
// <app-root ngCspNonce="__CSP_NONCE__"> and replaced on every response.
var noncePlaceholder = []byte("__CSP_NONCE__")

func serveIndex(w http.ResponseWriter, r *http.Request, static fs.FS) {
	page, err := fs.ReadFile(static, "index.html")
	if err != nil {
		writeError(w, r, http.StatusNotFound)
		return
	}
	page = bytes.ReplaceAll(page, noncePlaceholder, []byte(cspNonce(r.Context())))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}
