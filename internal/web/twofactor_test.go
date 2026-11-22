package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func TestPasswordOnlySignInAndTwoFactorSetup(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.70:4000"
	ctx := context.Background()
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 22, 16, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	at := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	const password = "correct-horse-1"
	hash, err := auth.Hash(password, h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	ada, err := h.st.CreateUser(ctx, store.NewUser{
		Email: "ada-2fa@example.com", DisplayName: "Ada", Workspace: "Personal",
		PasswordHash: hash, Theme: "dark",
	})
	if err != nil {
		t.Fatal(err)
	}
	blake, err := h.st.CreateUser(ctx, store.NewUser{
		Email: "blake-2fa@example.com", DisplayName: "Blake", Workspace: "Personal",
		PasswordHash: hash, Theme: "dark",
	})
	if err != nil {
		t.Fatal(err)
	}

	code, _, hdr := h.do(http.MethodGet, "/settings/two-factor", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("signed out setup %d %s", code, hdr.Get("Location"))
	}

	h.loginPassword("ada-2fa@example.com", password, ip)
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, ">Off</span>") || !strings.Contains(body, "Set up") || strings.Contains(body, "8 recovery codes") {
		t.Fatalf("settings off (%d bytes)", len(body))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/two-factor", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Two-factor is off") || !strings.Contains(body, `action="/settings/two-factor/start"`) || strings.Contains(body, "otpauth://") {
		t.Fatalf("setup form (%d bytes)", len(body))
	}
	code, body, _ = h.do(http.MethodPost, "/settings/two-factor/start", h.form(nil), ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `aria-label="Authenticator secret"`) || !strings.Contains(body, "otpauth://totp/") {
		t.Fatal("start did not show a secret")
	}
	secret := labeledValue(t, body, "Authenticator secret")
	code, again, _ := h.do(http.MethodGet, "/settings/two-factor", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(again, secret) {
		t.Fatal("refresh dropped the pending secret")
	}
	totp, err := auth.TOTPCode(secret, at())
	if err != nil {
		t.Fatal(err)
	}
	bad := "000000"
	if bad == totp {
		bad = "111111"
	}
	code, body, _ = h.do(http.MethodPost, "/settings/two-factor/confirm", h.form(mapForm("code", bad)), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "That code did not work") || !strings.Contains(body, secret) {
		t.Fatalf("bad code %d", code)
	}
	on, err := h.st.HasTOTP(ctx, ada.ID)
	if err != nil || on {
		t.Fatalf("ada enabled early %v %v", on, err)
	}

	h.logout(ip)
	h.loginPassword("blake-2fa@example.com", password, ip)
	code, body, _ = h.do(http.MethodGet, "/settings/two-factor", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, "otpauth://") {
		t.Fatal("blake saw ada secret")
	}
	code, body, _ = h.do(http.MethodPost, "/settings/two-factor/confirm", h.form(mapForm("code", totp)), ip, nil)
	if code != http.StatusBadRequest || strings.Contains(body, secret) {
		t.Fatalf("blake confirm %d", code)
	}
	on, err = h.st.HasTOTP(ctx, ada.ID)
	if err != nil || on {
		t.Fatalf("blake enabled ada %v %v", on, err)
	}
	on, err = h.st.HasTOTP(ctx, blake.ID)
	if err != nil || on {
		t.Fatalf("blake on %v %v", on, err)
	}
	pending, err := h.st.PendingTOTP(ctx, ada.ID)
	if err != nil || pending != secret {
		t.Fatalf("ada pending %q %v", pending, err)
	}

	h.logout(ip)
	h.loginPassword("ada-2fa@example.com", password, ip)
	code, body, hdr = h.do(http.MethodPost, "/settings/two-factor/confirm", h.form(mapForm("code", totp)), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings?notice=two-factor" {
		t.Fatalf("confirm %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings?notice=two-factor", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, ">On</span>") || !strings.Contains(body, "Two-factor is on.") || strings.Contains(body, secret) || strings.Contains(body, "8 recovery codes") {
		t.Fatalf("settings on (%d bytes)", len(body))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/two-factor", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, secret) || !strings.Contains(body, "Two-factor stays on") {
		t.Fatal("enabled page still showed the secret")
	}

	h.logout(ip)
	clockMu.Lock()
	clock = clock.Add(31 * time.Second)
	clockMu.Unlock()
	h.login("ada-2fa@example.com", password, secret, ip, false)
	on, err = h.st.HasTOTP(ctx, blake.ID)
	if err != nil || on {
		t.Fatalf("blake after ada login %v %v", on, err)
	}
}
