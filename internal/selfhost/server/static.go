package server

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	indexName      = "index.html"
	assetsPrefix   = "assets/"
	immutableCache = "public, max-age=31536000, immutable"
	maxStaticBytes = 16 << 20
)

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".map":         "application/json; charset=utf-8",
}

func apiPath(requestPath string) bool {
	return requestPath == "/v1" || strings.HasPrefix(requestPath, "/v1/") || requestPath == "/metrics" || requestPath == "/livez" || requestPath == "/readyz" || requestPath == "/config.json"
}

func (s *server) fallback(mux *http.ServeMux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if apiPath(r.URL.Path) {
			s.unmatchedAPI(w, r, mux)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This address allows only GET and HEAD.")
			return
		}
		s.serveStatic(w, r)
	}
}

func (s *server) unmatchedAPI(w http.ResponseWriter, r *http.Request, mux *http.ServeMux) {
	if allow, known := allowedMethods(r, mux); known {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "The requested API endpoint does not exist.")
}

var probeMethods = []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}

func allowedMethods(r *http.Request, mux *http.ServeMux) (string, bool) {
	var allow []string
	for _, method := range probeMethods {
		candidate := r.Clone(r.Context())
		candidate.Method = method
		if _, pattern := mux.Handler(candidate); pattern != "" && pattern != "/" {
			allow = append(allow, method)
		}
	}
	if len(allow) == 0 {
		return "", false
	}
	return strings.Join(allow, ", "), true
}

func (s *server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if rec, ok := w.(*recorder); ok {
		rec.route = "static"
	}
	if target, ok := trailingSlashTarget(r.URL); ok {
		w.Header().Set("Location", target)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = indexName
	}
	data, served, ok := s.readStatic(name)
	if !ok {
		if name != indexName && path.Ext(name) != "" {
			writeError(w, http.StatusNotFound, "not_found", "That file does not exist.")
			return
		}
		data, served, ok = s.readStatic(indexName)
		if !ok {
			writeError(w, http.StatusNotFound, "console_missing", "This server was built without a console.")
			return
		}
	}
	header := w.Header()
	header.Set("Content-Type", contentType(served))
	header.Set("Content-Security-Policy", consoleCSP)
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	if strings.HasPrefix(served, assetsPrefix) {
		header.Set("Cache-Control", immutableCache)
	} else {
		header.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

func trailingSlashTarget(u *url.URL) (string, bool) {
	escaped := u.EscapedPath()
	if len(escaped) < 2 || !strings.HasSuffix(escaped, "/") {
		return "", false
	}
	trimmed := strings.TrimLeft(strings.TrimRight(escaped, "/"), "/")
	if trimmed == "" {
		return "", false
	}
	target := "/" + trimmed
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target, true
}

func (s *server) readStatic(name string) ([]byte, string, bool) {
	if !fs.ValidPath(name) {
		return nil, "", false
	}
	info, err := fs.Stat(s.console, name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", false
	}
	file, err := s.console.Open(name)
	if err != nil {
		return nil, "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStaticBytes+1))
	if err != nil || len(data) > maxStaticBytes {
		return nil, "", false
	}
	return data, name, true
}

func contentType(name string) string {
	extension := strings.ToLower(path.Ext(name))
	if known, ok := contentTypes[extension]; ok {
		return known
	}
	if detected := mime.TypeByExtension(extension); detected != "" {
		return detected
	}
	return "application/octet-stream"
}
