package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
)

func TestWebCardHidesTheKeyFromAnotherUser(t *testing.T) {
	ip := "203.0.113.80:9"
	h := newHarness(t)
	owner, secret := h.account("web-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("web-other@example.com", "Bea", "correct-horse-1")
	h.login("web-owner@example.com", "correct-horse-1", secret, ip, false)
	const key = "wk-sample-search-key"
	code, body, hdr := h.do(http.MethodPost, "/settings/connections/web", h.form(url.Values{
		"token": {key},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=connected") || strings.Contains(body, key) {
		t.Fatalf("connect %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/connections/web", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Search key") || !strings.Contains(body, "web_search") || !strings.Contains(body, "web_read") || strings.Contains(body, key) {
		t.Fatalf("detail %d", code)
	}
	if !strings.Contains(body, "wk-s-xxxxxxx") {
		t.Fatal("mask")
	}
	saved, err := h.st.ConnectionValue(context.Background(), owner.ID, connect.Web)
	if err != nil || saved != key {
		t.Fatal("owner vault")
	}
	if _, err := h.st.ConnectionValue(context.Background(), other.ID, connect.Web); err == nil {
		t.Fatal("other user read the key")
	}
	if err := h.st.EnableConnection(context.Background(), other.ID, "missing-agent", connect.Web); err == nil {
		t.Fatal("other user enabled web")
	}
}

func TestLinearCardHidesTheKeyFromAnotherUser(t *testing.T) {
	ip := "203.0.113.81:9"
	h := newHarness(t)
	owner, secret := h.account("linear-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("linear-other@example.com", "Bea", "correct-horse-1")
	h.login("linear-owner@example.com", "correct-horse-1", secret, ip, false)
	const key = "lin_sample_api_key_value"
	code, body, hdr := h.do(http.MethodPost, "/settings/connections/linear", h.form(url.Values{
		"token": {key},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=connected") || strings.Contains(body, key) {
		t.Fatalf("connect %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/connections/linear", "", ip, nil)
	if code != 200 || !strings.Contains(body, "API key") || !strings.Contains(body, "linear_create_issue") || !strings.Contains(body, "linear_list_issues") || strings.Contains(body, key) {
		t.Fatalf("detail %d", code)
	}
	if !strings.Contains(body, "lin_-xxxxxxx") {
		t.Fatal("mask")
	}
	saved, err := h.st.ConnectionValue(context.Background(), owner.ID, connect.Linear)
	if err != nil || saved != key {
		t.Fatal("owner vault")
	}
	if _, err := h.st.ConnectionValue(context.Background(), other.ID, connect.Linear); err == nil {
		t.Fatal("other user read the key")
	}
}
