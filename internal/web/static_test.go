package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestStaticVersionedCacheAndETag(t *testing.T) {
	h := newHarness(t)
	pageCode, page, _ := h.do(http.MethodGet, "/login", "", "", nil)
	if pageCode != http.StatusOK {
		t.Fatal(pageCode)
	}
	cssURL := staticHref("castbot.css")
	if !strings.Contains(page, `href="`+cssURL+`"`) {
		t.Fatalf("login page missing %s", cssURL)
	}
	if strings.Contains(page, `href="/static/castbot.css"`) {
		t.Fatal("login page still uses an unversioned stylesheet")
	}
	for _, name := range []string{
		"fonts/hanken-grotesk.woff2",
		"fonts/bricolage-grotesque.woff2",
		"app.js",
		"create.css",
		"htmx.min.js",
	} {
		url := staticHref(name)
		if !strings.Contains(page, url) {
			t.Fatalf("login page missing %s", url)
		}
	}
	if !strings.Contains(page, `rel="preload"`) || !strings.Contains(page, `as="font"`) {
		t.Fatal("interface fonts are not preloaded")
	}
	_, secret := h.account("static-cache@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.44:9"
	h.login("static-cache@example.com", "correct-horse-1", secret, ip, false)
	_, appPage, _ := h.do(http.MethodGet, "/", "", ip, nil)
	for _, name := range []string{"sse.js", "stream.js", "secrets.js", "files.js", "create.js", "htmx.min.js"} {
		url := staticHref(name)
		if !strings.Contains(appPage, url) {
			t.Fatalf("app shell missing %s", url)
		}
		if strings.Contains(appPage, `src="/static/`+name+`"`) {
			t.Fatalf("app shell has an unversioned %s", name)
		}
	}

	code, body, hdr := h.do(http.MethodGet, cssURL, "", "", nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if hdr.Get("Cache-Control") != staticCacheImmutable {
		t.Fatalf("cache %q", hdr.Get("Cache-Control"))
	}
	sum := sha256.Sum256([]byte(body))
	wantTag := hex.EncodeToString(sum[:8])
	if !strings.HasSuffix(cssURL, "?v="+wantTag) {
		t.Fatalf("url %s is not the hash of the served bytes", cssURL)
	}
	etag := hdr.Get("ETag")
	if etag != `"`+wantTag+`"` {
		t.Fatalf("etag %q", etag)
	}
	if hdr.Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("content type %q", hdr.Get("Content-Type"))
	}
	font := staticHref("fonts/hanken-grotesk.woff2")
	if !strings.Contains(body, "fonts/hanken-grotesk.woff2?v="+staticAssets["fonts/hanken-grotesk.woff2"].tag) {
		t.Fatal("stylesheet font url is not versioned")
	}
	raw, err := staticFS.ReadFile("static/castbot.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "?v=") {
		t.Fatal("embedded source was rewritten on disk")
	}

	code, cached, hdr := h.do(http.MethodGet, cssURL, "", "", map[string]string{"If-None-Match": etag})
	if code != http.StatusNotModified {
		t.Fatalf("revalidate %d", code)
	}
	if cached != "" {
		t.Fatalf("304 body %q", cached)
	}
	if hdr.Get("ETag") != etag || hdr.Get("Cache-Control") != staticCacheImmutable {
		t.Fatalf("304 headers etag %q cache %q", hdr.Get("ETag"), hdr.Get("Cache-Control"))
	}

	code, fontBody, fontHdr := h.do(http.MethodGet, font, "", "", nil)
	if code != http.StatusOK || fontHdr.Get("Cache-Control") != staticCacheImmutable {
		t.Fatalf("font %d %q", code, fontHdr.Get("Cache-Control"))
	}
	if fontHdr.Get("Content-Type") != "font/woff2" || len(fontBody) == 0 {
		t.Fatalf("font type %q len %d", fontHdr.Get("Content-Type"), len(fontBody))
	}

	code, plain, plainHdr := h.do(http.MethodGet, "/static/app.js", "", "", nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if plainHdr.Get("Cache-Control") != staticCacheRevalidate {
		t.Fatalf("unversioned cache %q", plainHdr.Get("Cache-Control"))
	}
	plainTag := plainHdr.Get("ETag")
	if plainTag == "" || !strings.Contains(plain, "function closeAgentPanel") {
		t.Fatalf("etag %q", plainTag)
	}
	code, again, _ := h.do(http.MethodGet, "/static/app.js", "", "", map[string]string{"If-None-Match": plainTag})
	if code != http.StatusNotModified || again != "" {
		t.Fatalf("unversioned 304 %d %q", code, again)
	}

	code, mismatch, mismatchHdr := h.do(http.MethodGet, "/static/app.js?v=0000000000000000", "", "", nil)
	if code != http.StatusOK || mismatch == "" {
		t.Fatalf("stale hash %d", code)
	}
	if mismatchHdr.Get("Cache-Control") != staticCacheRevalidate {
		t.Fatalf("stale hash cached as immutable: %q", mismatchHdr.Get("Cache-Control"))
	}
	if mismatch != plain {
		t.Fatal("stale hash served different bytes")
	}
}

func TestStaticSkipsSession(t *testing.T) {
	h := newHarness(t)
	code, _, hdr := h.do(http.MethodGet, "/static/app.js", "", "", map[string]string{
		"Cookie": "castbot_session=not-a-session",
	})
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if hdr.Get("Set-Cookie") != "" {
		t.Fatalf("static set a cookie: %q", hdr.Get("Set-Cookie"))
	}

	_, _, loginHdr := h.do(http.MethodGet, "/login", "", "203.0.113.50:9", nil)
	if cookieLine(loginHdr, "castbot_csrf") == "" {
		t.Fatal("login no longer issues a csrf cookie")
	}

	code, body, _ := h.do(http.MethodGet, "/static/", "", "", nil)
	if code != http.StatusNotFound || strings.Contains(body, "castbot.css") {
		t.Fatalf("directory %d %q", code, body)
	}
	code, _, _ = h.do(http.MethodGet, "/static/fonts/", "", "", nil)
	if code != http.StatusNotFound {
		t.Fatalf("font dir %d", code)
	}
}

func TestStaticPathClean(t *testing.T) {
	if staticRequest("/static/../login") {
		t.Fatal("escaped static path skipped the session")
	}
	if !staticRequest("/static/app.js") || !staticRequest("/static/fonts/hanken-grotesk.woff2") {
		t.Fatal("real static paths were not recognized")
	}
	h := newHarness(t)
	code, _, hdr := h.do(http.MethodGet, "/static/../login", "", "", map[string]string{
		"Cookie": "castbot_session=not-a-session",
	})
	if cookieLine(hdr, "castbot_csrf") == "" {
		t.Fatalf("escaped path %d skipped the session, cookie %q", code, hdr.Get("Set-Cookie"))
	}
	if code != http.StatusOK && code != http.StatusTemporaryRedirect {
		t.Fatalf("escaped path %d", code)
	}
}
