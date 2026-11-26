package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

const hostedDoc = `{"v":1,"blocks":[
  {"type":"control","id":"range","label":"Range","options":["7 days","30 days"]},
  {"type":"markdown","text":"Week view","when":{"range":"7 days"}},
  {"type":"markdown","text":"Month view <script>alert(1)</script>","when":{"range":"30 days"}},
  {"type":"chart","kind":"bar","title":"Trials","series":[{"name":"Trials","points":[{"x":"Mon","y":3},{"x":"Tue","y":1}]}]},
  {"type":"chart","kind":"line","title":"Trend","series":[{"name":"Trend","points":[{"x":"Mon","y":3},{"x":"Tue","y":1}]}]},
  {"type":"table","columns":["Day","Trials"],"rows":[{"cells":["Mon","3"]}]}
]}`

func TestHostedPageAccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A TOTP step is accepted once. Later sign-ins in this test move the clock.
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return clock })
	advance := func() { clock = clock.Add(2 * time.Minute) }
	owner, secret := h.account("hosted-owner@example.com", "Alex Rivera", "correct-horse-1")
	other, otherSecret := h.account("hosted-other@example.com", "Blair Quinn", "correct-horse-1")
	member := acceptHostedInvite(t, h, owner, "hosted-member@example.com", "Pip Lane")
	agent := hostedAgent(t, h.st, owner, "Atlas")
	made, err := h.st.CreatePage(ctx, owner.ID, agent.ID, "Trials", hostedDoc)
	if err != nil {
		t.Fatal(err)
	}

	code, body, hdr := h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.20:4000", nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("private anon %d %s", code, hdr.Get("Location"))
	}
	if hdr.Get("Set-Cookie") != "" {
		t.Fatalf("private anon set a cookie")
	}

	code, body, _ = h.do(http.MethodGet, "/p/not-a-token", "", "203.0.113.21:4000", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("unknown %d %s", code, body)
	}

	h.login(owner.Email, "correct-horse-1", secret, "203.0.113.22:4000", false)
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.22:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "Trials") || !strings.Contains(body, "Week view") || !strings.Contains(body, "<svg") || !strings.Contains(body, "<polyline") {
		t.Fatalf("owner %d", code)
	}
	if strings.Contains(body, "<script>alert") || strings.Contains(body, owner.Email) || strings.Contains(body, "Alex Rivera") {
		t.Fatal("page leaked a script or the owner")
	}
	if !strings.Contains(body, "Month view") || !strings.Contains(body, `data-when="{&#34;range&#34;:&#34;30 days&#34;}" hidden`) {
		t.Fatal("default filter did not hide the other block")
	}
	if strings.Contains(body, `data-when="{&#34;range&#34;:&#34;7 days&#34;}" hidden`) {
		t.Fatal("default block was hidden")
	}

	advance()
	h.switchUser(other.Email, "correct-horse-1", otherSecret, "203.0.113.23:4000")
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.23:4000", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("other user %d %s", code, body)
	}

	if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageWorkspace, nil, nil); err != nil {
		t.Fatal(err)
	}
	advance()
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.24:4000")
	code, _, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.24:4000", nil)
	if code != http.StatusOK {
		t.Fatalf("workspace member %d", code)
	}
	advance()
	h.switchUser(other.Email, "correct-horse-1", otherSecret, "203.0.113.23:4000")
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.23:4000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("other workspace %d %s", code, body)
	}

	if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageInvited, []string{member.ID}, nil); err != nil {
		t.Fatal(err)
	}
	advance()
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.24:4000")
	code, _, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.24:4000", nil)
	if code != http.StatusOK {
		t.Fatalf("invited %d", code)
	}
	outsider := acceptHostedInvite(t, h, owner, "hosted-outsider@example.com", "Casey Drew")
	advance()
	h.switchUser(outsider.Email, "correct-horse-1", outsider.secret, "203.0.113.25:4000")
	code, _, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.25:4000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("uninvited member %d", code)
	}

	if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageLink, nil, nil); err != nil {
		t.Fatal(err)
	}
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	code, body, hdr = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.26:4000", nil)
	if code != http.StatusOK || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("public %d cookie %q", code, hdr.Get("Set-Cookie"))
	}
	if strings.Contains(body, owner.Email) || strings.Contains(body, "Alex Rivera") || strings.Contains(body, "<script>alert") {
		t.Fatal("public page leaked")
	}
	if !strings.Contains(body, "noindex") || hdr.Get("X-Robots-Tag") != "noindex" || hdr.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("headers %v", hdr)
	}

	if err := h.st.DeletePage(ctx, owner.ID, "", made.Token); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.26:4000", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("deleted %d %s", code, body)
	}
}

func (h *harness) switchUser(email, password, secret, ip string) {
	h.t.Helper()
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login(email, password, secret, ip, false)
}

func hostedAgent(t *testing.T, st *store.Store, user store.User, name string) store.Agent {
	t.Helper()
	sec, err := st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

type hostedMember struct {
	store.User
	secret string
}

func acceptHostedInvite(t *testing.T, h *harness, owner store.User, email, name string) hostedMember {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.Hash("correct-horse-1", h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := h.st.CreateInvite(ctx, owner.ID, email, name)
	if err != nil {
		t.Fatal(err)
	}
	member, err := h.st.AcceptInvite(ctx, token, hash, secret)
	if err != nil {
		t.Fatal(err)
	}
	return hostedMember{User: member, secret: secret}
}
