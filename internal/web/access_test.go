package web

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestLogFieldsOmitSecrets(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))

	req := httptest.NewRequest(http.MethodGet, "/login?token=sekrit-query-value&next=/private", nil)
	req.AddCookie(&http.Cookie{Name: "castbot_session", Value: "sekrit-cookie-value"})
	req.Header.Set("Authorization", "Bearer sekrit-header-token")
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d", rec.Code)
	}
	line := buf.String()
	assertRequestLine(t, line, "GET", "/login", 200)
	assertNoSecrets(t, line)

	buf.Reset()
	staticReq := httptest.NewRequest(http.MethodGet, "/static/app.js?v=sekrit-static-query", nil)
	staticReq.AddCookie(&http.Cookie{Name: "castbot_session", Value: "sekrit-cookie-value"})
	h.srv.Handler().ServeHTTP(httptest.NewRecorder(), staticReq)
	if buf.Len() != 0 {
		t.Fatalf("static was logged: %s", buf.String())
	}
}

func TestRequestLogStatusAndBody(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{}
	s.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	h := s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "body-secret nope", http.StatusNotFound)
	}))
	req := httptest.NewRequest(http.MethodPost, "/missing?x=sekrit-query-value", strings.NewReader("posted-body-secret"))
	req.Header.Set("Cookie", "castbot_session=sekrit-cookie-value")
	h.ServeHTTP(httptest.NewRecorder(), req)
	line := buf.String()
	assertRequestLine(t, line, "POST", "/missing", 404)
	assertNoSecrets(t, line)
	if strings.Contains(line, "body-secret") || strings.Contains(line, "posted-body") || strings.Contains(line, "nope") {
		t.Fatalf("log included a body: %s", line)
	}
}

func TestRequestLogSSEAtClose(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{}
	s.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	h := s.logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, ": ka\n\n")
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, ": ka\n\n")
	}))
	req := httptest.NewRequest(http.MethodGet, "/runs/abc/events?token=sekrit-query-value", nil)
	req.AddCookie(&http.Cookie{Name: "castbot_session", Value: "sekrit-cookie-value"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != ": ka\n\n: ka\n\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
	line := buf.String()
	assertRequestLine(t, line, "GET", "/runs/abc/events", 200)
	assertNoSecrets(t, line)
	if strings.Contains(line, ": ka") || strings.Contains(line, "ka") {
		t.Fatalf("keepalive was logged: %s", line)
	}
	if d := durationMS(line); d < 20 {
		t.Fatalf("duration_ms %d in %s", d, line)
	}
}

func assertRequestLine(t *testing.T, line, method, path string, status int) {
	t.Helper()
	if strings.Count(line, "msg=request") != 1 {
		t.Fatalf("want one request line, got %q", line)
	}
	for _, part := range []string{
		"method=" + method,
		"path=" + path,
		"status=" + itoa(status),
		"duration_ms=",
	} {
		if !strings.Contains(line, part) {
			t.Fatalf("missing %s in %s", part, line)
		}
	}
}

func assertNoSecrets(t *testing.T, line string) {
	t.Helper()
	for _, secret := range []string{
		"sekrit-query-value",
		"sekrit-cookie-value",
		"sekrit-header-token",
		"sekrit-static-query",
		"castbot_session",
		"Bearer",
		"?",
		"token=",
		"Cookie",
	} {
		if strings.Contains(line, secret) {
			t.Fatalf("log leaked %q in %s", secret, line)
		}
	}
}

func durationMS(line string) int {
	const marker = "duration_ms="
	i := strings.Index(line, marker)
	if i < 0 {
		return -1
	}
	n := 0
	for _, c := range line[i+len(marker):] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
