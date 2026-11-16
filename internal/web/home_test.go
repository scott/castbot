package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestHomeOpensPreferredChat(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.104:9"
	code, _, hdr := h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("signed out home %d %s", code, hdr.Get("Location"))
	}
	code, _, hdr = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("signed out list %d %s", code, hdr.Get("Location"))
	}

	user, secret := h.account("home-104@example.com", "Alex Rivera", "correct-horse-1")
	h.login("home-104@example.com", "correct-horse-1", secret, ip, false)
	wantHome(t, h, ip, "/new-agent")
	code, _, hdr = h.do(http.MethodGet, "/?list=0", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/new-agent" {
		t.Fatalf("other list query %d %s", code, hdr.Get("Location"))
	}
	code, list, _ := h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Contains(list, "Pick an agent") || strings.Contains(list, "Create your first agent") {
		t.Fatalf("empty list %d", code)
	}
	for _, bit := range []string{`aria-label="New agent"`, `href="/settings"`, `aria-label="Settings"`, `aria-label="castbot home"`, `href="/"`, "No agents yet", "Alex Rivera"} {
		if !strings.Contains(list, bit) {
			t.Fatalf("empty list missing %s", bit)
		}
	}
	code, create, _ := h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(create, `href="/?list=1"`) || !strings.Contains(create, `aria-label="Back to agents"`) {
		t.Fatal("new-agent back does not open the agent list")
	}

	same := time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)
	ada := homeAgent(t, h, user.ID, "Ada", same)
	bea := homeAgent(t, h, user.ID, "Bea", same)
	tie := ada
	if bea.ID > ada.ID {
		tie = bea
	}
	wantHome(t, h, ip, "/agents/"+tie.ID)

	older := homeAgent(t, h, user.ID, "Cara", time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC))
	newer := homeAgent(t, h, user.ID, "Dora", time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC))
	wantHome(t, h, ip, "/agents/"+newer.ID)

	code, chat, _ := h.do(http.MethodGet, "/agents/"+older.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(chat, `href="/?list=1"`) || !strings.Contains(chat, `aria-label="Back to agents"`) || strings.Contains(chat, "Pick an agent") {
		t.Fatalf("open chat %d", code)
	}
	wantHome(t, h, ip, "/agents/"+older.ID)
	code, found, _ := h.do(http.MethodGet, "/?q=Dora", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(found, "Dora") || strings.Contains(found, "Pick an agent") {
		t.Fatalf("home search %d", code)
	}
	wantHome(t, h, ip, "/agents/"+older.ID)
	code, list, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Contains(list, "Pick an agent") || !strings.Contains(list, "Cara") || !strings.Contains(list, "Dora") || !strings.Contains(list, "Ada") || !strings.Contains(list, "Bea") {
		t.Fatal("agent list dropped a name or showed the pick heading")
	}
	wantHome(t, h, ip, "/agents/"+older.ID)

	latest := homeAgent(t, h, user.ID, "Eve", time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC))
	wantHome(t, h, ip, "/agents/"+older.ID)

	code, sent, _ := h.do(http.MethodPost, "/agents/"+newer.ID+"/messages", h.form(mapForm("body", "Draft the weekly note.")), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || !strings.Contains(sent, "Draft the weekly note.") {
		t.Fatalf("send %d", code)
	}
	wantHome(t, h, ip, "/agents/"+newer.ID)
	code, _, _ = h.do(http.MethodPost, "/agents/"+older.ID+"/messages", h.form(mapForm("body", "   ")), ip, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("blank send %d", code)
	}
	wantHome(t, h, ip, "/agents/"+newer.ID)

	code, body, _ := h.do(http.MethodPost, "/agents/"+newer.ID+"/delete", h.form(nil), ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "ok") {
		t.Fatalf("delete recent %d %s", code, body)
	}
	wantHome(t, h, ip, "/agents/"+older.ID)
	for _, id := range []string{ada.ID, bea.ID, older.ID, latest.ID} {
		code, body, _ = h.do(http.MethodPost, "/agents/"+id+"/delete", h.form(nil), ip, nil)
		if code != http.StatusOK {
			t.Fatalf("delete %s %d %s", id, code, body)
		}
	}
	wantHome(t, h, ip, "/new-agent")

	code, _, hdr = h.do(http.MethodGet, "/login", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("signed-in login %d %s", code, hdr.Get("Location"))
	}
	wantHome(t, h, ip, "/new-agent")

	only := homeAgent(t, h, user.ID, "Faye", time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC))
	code, _, _ = h.do(http.MethodGet, "/agents/"+only.ID, "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	code, _, hdr = h.do(http.MethodGet, "/login", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("signed-in login with an agent %d %s", code, hdr.Get("Location"))
	}
	wantHome(t, h, ip, "/agents/"+only.ID)

	other, otherSecret := h.account("home-other-104@example.com", "Blair Quinn", "correct-horse-1")
	theirs := homeAgent(t, h, other.ID, "Gina", time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC))
	h.logout(ip)
	h.login("home-other-104@example.com", "correct-horse-1", otherSecret, ip, false)
	code, _, _ = h.do(http.MethodGet, "/agents/"+only.ID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other open %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+newer.ID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other open deleted %d", code)
	}
	wantHome(t, h, ip, "/agents/"+theirs.ID)
	code, list, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Contains(list, "Faye") || strings.Contains(list, only.ID) || !strings.Contains(list, "Gina") || strings.Contains(list, "Pick an agent") {
		t.Fatal("other list saw this account's agents")
	}
	got, err := h.st.PreferredAgent(context.Background(), user.ID)
	if err != nil || got.ID != only.ID {
		t.Fatalf("other user changed the owner preferred %s %v", got.ID, err)
	}
}

func wantHome(t *testing.T, h *harness, ip, loc string) {
	t.Helper()
	code, _, hdr := h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != loc {
		t.Fatalf("home %d %s want %s", code, hdr.Get("Location"), loc)
	}
}

func homeAgent(t *testing.T, h *harness, userID, name string, at time.Time) store.Agent {
	t.Helper()
	sec, err := h.st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}
