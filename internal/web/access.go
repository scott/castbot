package web

import (
	"net/http"
	"path"
	"strings"
	"time"
)

// logRequests writes one line when the handler returns: method, path, status, duration_ms.
// The path has no query string. Bodies, headers, cookies, and query values are not logged.
// /static/ is skipped. A server-sent stream stays open inside the handler, so this
// line is written once, at close, and keepalive comments are not separate lines.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			p := requestLogPath(r.URL.Path)
			if p == "/static" || strings.HasPrefix(p, "/static/") {
				return
			}
			s.logger().Info("request",
				"method", r.Method,
				"path", p,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}()
		next.ServeHTTP(sw, r)
	})
}

// requestLogPath is the URL path only. Query strings stay off the log line.
func requestLogPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
