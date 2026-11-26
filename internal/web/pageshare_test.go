package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestPageShareRenameDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	advance := func() {
		clockMu.Lock()
		clock = clock.Add(2 * time.Minute)
		clockMu.Unlock()
	}
	owner, secret := h.account("share-owner@example.com", "Nora Hale", "correct-horse-1")
	other, otherSecret := h.account("share-other@example.com", "Blair Quinn", "correct-horse-1")
	member := acceptHostedInvite(t, h, owner, "share-member@example.com", "Pip Lane")
	outsider := acceptHostedInvite(t, h, owner, "share-outsider@example.com", "Casey Drew")
	atlas := hostedAgent(t, h.st, owner, "Atlas")
	moss := hostedAgent(t, h.st, member.User, "Moss")
	made := settleOwnedPage(t, h, owner.ID, atlas, "North desk notes", samplePageText("Hello from the north desk."))
	token := made.Token
	path := "/p/" + token

	code, _, hdr := h.do(http.MethodGet, path, "", "203.0.113.90:4000", nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("anon %d %s", code, hdr.Get("Location"))
	}

	h.login(owner.Email, "correct-horse-1", secret, "203.0.113.91:4000", false)
	code, body, _ := h.do(http.MethodGet, path, "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "North desk notes") || !strings.Contains(body, "Hello from the north desk.") {
		t.Fatalf("owner page %d", code)
	}
	for _, want := range []string{"Rename", "Who can view", "Only you", "People you invite", "Anyone with the link", "Save sharing", "Copy link", "Delete page", "This link will stop opening the page.", "Pip Lane", "Moss", "Atlas", ">Personal</span>", `value="private" checked`} {
		if !strings.Contains(body, want) {
			t.Fatalf("owner controls missing %q", want)
		}
	}
	if !strings.Contains(body, "http://example.com/p/"+token) {
		t.Fatal("copy field is not this page")
	}
	for _, banned := range []string{"Nora Hale", owner.Email, "only_you", "<script>alert"} {
		if strings.Contains(body, banned) {
			t.Fatalf("owner page leaked %q", banned)
		}
	}
	code, body, hdr = h.do(http.MethodGet, path, "", "203.0.113.91:4000", map[string]string{"X-Forwarded-Proto": "https"})
	if code != http.StatusOK || !strings.Contains(body, "https://example.com/p/"+token) {
		t.Fatal("forwarded link")
	}

	code, body, _ = h.do(http.MethodGet, "/agents/"+atlas.ID, "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, `class="t2">Only you</span>`) {
		t.Fatal("card subtitle")
	}

	before := currentPage(t, h, owner.ID, made.ID)
	code, body, _ = postForm(h, path+"/rename", "203.0.113.91:4000", map[string][]string{"title": {""}})
	if code != http.StatusBadRequest || !strings.Contains(body, "Enter a title up to 80 characters.") {
		t.Fatalf("empty title %d", code)
	}
	before.same(t, h, owner.ID, made.ID)
	code, _, _ = postForm(h, path+"/rename", "203.0.113.91:4000", map[string][]string{"title": {"Line\nbreak"}})
	if code != http.StatusBadRequest {
		t.Fatalf("broken title %d", code)
	}
	before.same(t, h, owner.ID, made.ID)

	code, body, _ = h.do(http.MethodPost, path+"/rename", "title=Stolen", "203.0.113.91:4000", nil)
	if code != http.StatusForbidden || !strings.Contains(body, "form expired") {
		t.Fatalf("csrf %d", code)
	}
	before.same(t, h, owner.ID, made.ID)

	code, _, hdr = postForm(h, path+"/rename", "203.0.113.91:4000", map[string][]string{"title": {"Desk notes"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != path+"?notice=renamed" {
		t.Fatalf("rename %d", code)
	}
	renamed := currentPage(t, h, owner.ID, made.ID)
	if renamed.title != "Desk notes" || renamed.token != token || renamed.rev != before.rev {
		t.Fatal("rename did not keep the token")
	}
	code, body, _ = h.do(http.MethodGet, path+"?notice=renamed", "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "Title saved.") || !strings.Contains(body, "<h1>Desk notes</h1>") || strings.Contains(body, "<h1>North desk notes</h1>") {
		t.Fatal("renamed page")
	}

	code, body, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"invited"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "Say who should see it.") {
		t.Fatalf("empty invite %d", code)
	}
	renamed.same(t, h, owner.ID, made.ID)
	code, body, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"public"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "Choose who can view this page.") {
		t.Fatalf("bad visibility %d", code)
	}
	renamed.same(t, h, owner.ID, made.ID)
	code, body, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"invited"}, "agent": {atlas.ID}})
	if code != http.StatusBadRequest || !strings.Contains(body, "Say who should see it.") {
		t.Fatalf("creating agent only %d", code)
	}
	renamed.same(t, h, owner.ID, made.ID)

	code, _, hdr = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"workspace"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != path+"?notice=shared" {
		t.Fatalf("workspace share %d", code)
	}
	if grants := grantSet(t, h, owner.ID, made.ID); len(grants) != 0 {
		t.Fatalf("workspace grants %d", len(grants))
	}
	shared := currentPage(t, h, owner.ID, made.ID)
	if shared.vis != store.PageWorkspace || shared.token != token {
		t.Fatal("workspace visibility")
	}
	code, body, _ = h.do(http.MethodGet, path+"?notice=shared", "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "Sharing saved.") || !strings.Contains(body, `value="workspace" checked`) || strings.Contains(body, `value="private" checked`) {
		t.Fatal("workspace form")
	}
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, `class="t2">Personal</span>`) || strings.Contains(body, "Only you") {
		t.Fatal("list subtitle")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+atlas.ID, "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, `class="t2">Personal</span>`) || strings.Contains(body, "Only you") {
		t.Fatal("card subtitle after share")
	}

	code, _, hdr = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{
		"visibility": {"invited"}, "person": {member.ID}, "agent": {moss.ID},
	})
	if code != http.StatusSeeOther || hdr.Get("Location") != path+"?notice=shared" {
		t.Fatalf("invite %d", code)
	}
	grants := grantSet(t, h, owner.ID, made.ID)
	if !grants["user/"+member.ID] || !grants["agent/"+moss.ID] || grants["user/"+owner.ID] || grants["agent/"+atlas.ID] || len(grants) != 2 {
		t.Fatal("invite grants")
	}
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, `class="t2">People you invited</span>`) {
		t.Fatal("invited subtitle")
	}

	advance()
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.92:4000")
	code, body, _ = h.do(http.MethodGet, path, "", "203.0.113.92:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "Hello from the north desk.") || strings.Contains(body, "hosted-owner") || strings.Contains(body, "Rename") {
		t.Fatalf("member read %d", code)
	}
	advance()
	h.switchUser(outsider.Email, "correct-horse-1", outsider.secret, "203.0.113.93:4000")
	code, _, _ = h.do(http.MethodGet, path, "", "203.0.113.93:4000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("uninvited %d", code)
	}

	advance()
	h.switchUser(owner.Email, "correct-horse-1", secret, "203.0.113.91:4000")
	code, _, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{
		"visibility": {"invited"}, "person": {outsider.ID},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("replace invite %d", code)
	}
	grants = grantSet(t, h, owner.ID, made.ID)
	if !grants["user/"+outsider.ID] || len(grants) != 1 {
		t.Fatal("invite was not replaced")
	}
	advance()
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.92:4000")
	code, _, _ = h.do(http.MethodGet, path, "", "203.0.113.92:4000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("old invite %d", code)
	}
	advance()
	h.switchUser(outsider.Email, "correct-horse-1", outsider.secret, "203.0.113.93:4000")
	code, _, _ = h.do(http.MethodGet, path, "", "203.0.113.93:4000", nil)
	if code != http.StatusOK {
		t.Fatalf("new invite %d", code)
	}

	advance()
	h.switchUser(owner.Email, "correct-horse-1", secret, "203.0.113.91:4000")
	code, _, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"link"}})
	if code != http.StatusSeeOther {
		t.Fatalf("link %d", code)
	}
	if grants = grantSet(t, h, owner.ID, made.ID); len(grants) != 0 {
		t.Fatal("link left grants")
	}
	if currentPage(t, h, owner.ID, made.ID).vis != store.PageLink {
		t.Fatal("link visibility")
	}
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, `class="t2">Anyone with the link</span>`) {
		t.Fatal("link subtitle")
	}
	code, body, _ = h.do(http.MethodGet, path, "", "203.0.113.91:4000", nil)
	if code != http.StatusOK || !strings.Contains(body, "hosted-owner") || !strings.Contains(body, `value="link" checked`) {
		t.Fatal("owner of a link page")
	}

	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	code, body, hdr = h.do(http.MethodGet, path, "", "203.0.113.94:4000", nil)
	if code != http.StatusOK || hdr.Get("Set-Cookie") != "" || strings.Contains(body, "hosted-owner") || strings.Contains(body, "Rename") || strings.Contains(body, "Nora Hale") || strings.Contains(body, owner.Email) || strings.Contains(body, `name="csrf"`) {
		t.Fatalf("public %d", code)
	}
	if !strings.Contains(body, "Hello from the north desk.") {
		t.Fatal("public body")
	}

	advance()
	h.switchUser(other.Email, "correct-horse-1", otherSecret, "203.0.113.95:4000")
	code, body, _ = h.do(http.MethodGet, path, "", "203.0.113.95:4000", nil)
	if code != http.StatusOK || strings.Contains(body, "hosted-owner") || strings.Contains(body, "Rename") || !strings.Contains(body, "Hello from the north desk.") {
		t.Fatalf("other visitor %d", code)
	}
	held := currentPage(t, h, owner.ID, made.ID)
	for _, spec := range []struct {
		suffix string
		vals   map[string][]string
	}{
		{"/rename", map[string][]string{"title": {"Stolen"}}},
		{"/share", map[string][]string{"visibility": {"private"}}},
		{"/delete", map[string][]string{"confirm": {"yes"}}},
	} {
		code, body, _ = postForm(h, path+spec.suffix, "203.0.113.95:4000", spec.vals)
		if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
			t.Fatalf("other %s %d", spec.suffix, code)
		}
		held.same(t, h, owner.ID, made.ID)
	}

	advance()
	h.switchUser(owner.Email, "correct-horse-1", secret, "203.0.113.91:4000")
	code, _, _ = postForm(h, path+"/share", "203.0.113.91:4000", map[string][]string{"visibility": {"workspace"}})
	if code != http.StatusSeeOther {
		t.Fatalf("workspace again %d", code)
	}
	advance()
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.92:4000")
	code, body, _ = h.do(http.MethodGet, path, "", "203.0.113.92:4000", nil)
	if code != http.StatusOK || strings.Contains(body, "Rename") {
		t.Fatalf("workspace member %d", code)
	}
	held = currentPage(t, h, owner.ID, made.ID)
	code, body, _ = postForm(h, path+"/share", "203.0.113.92:4000", map[string][]string{"visibility": {"link"}})
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("member share %d", code)
	}
	held.same(t, h, owner.ID, made.ID)
	code, _, _ = postForm(h, path+"/rename", "203.0.113.92:4000", map[string][]string{"title": {"Stolen"}})
	if code != http.StatusNotFound {
		t.Fatalf("member rename %d", code)
	}
	code, _, _ = postForm(h, path+"/delete", "203.0.113.92:4000", map[string][]string{"confirm": {"yes"}})
	if code != http.StatusNotFound {
		t.Fatalf("member delete %d", code)
	}
	held.same(t, h, owner.ID, made.ID)

	advance()
	h.switchUser(other.Email, "correct-horse-1", otherSecret, "203.0.113.95:4000")
	code, _, _ = h.do(http.MethodGet, path, "", "203.0.113.95:4000", nil)
	if code != http.StatusNotFound {
		t.Fatalf("other workspace %d", code)
	}
	code, _, _ = postForm(h, path+"/share", "203.0.113.95:4000", map[string][]string{"visibility": {"link"}})
	if code != http.StatusNotFound {
		t.Fatalf("other workspace share %d", code)
	}
	held.same(t, h, owner.ID, made.ID)

	advance()
	h.switchUser(owner.Email, "correct-horse-1", secret, "203.0.113.91:4000")
	code, body, _ = postForm(h, path+"/delete", "203.0.113.91:4000", map[string][]string{})
	if code != http.StatusBadRequest || !strings.Contains(body, "Confirm the delete.") {
		t.Fatalf("unconfirmed delete %d", code)
	}
	held.same(t, h, owner.ID, made.ID)
	code, _, hdr = postForm(h, path+"/delete", "203.0.113.91:4000", map[string][]string{"confirm": {"yes"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != path {
		t.Fatalf("delete %d", code)
	}
	if _, err := h.st.PageForUser(ctx, owner.ID, made.ID); err != store.ErrNotFound {
		t.Fatal("deleted page still loads")
	}
	code, body, _ = h.do(http.MethodGet, path, "", "203.0.113.91:4000", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") || strings.Contains(body, "Desk notes") {
		t.Fatalf("deleted link %d", code)
	}
}

func postForm(h *harness, path, ip string, vals map[string][]string) (int, string, http.Header) {
	h.t.Helper()
	return h.do(http.MethodPost, path, h.form(url.Values(vals)), ip, nil)
}

type pageState struct {
	title, token, vis string
	rev               int
}

func currentPage(t *testing.T, h *harness, userID, id string) pageState {
	t.Helper()
	row, err := h.st.PageForUser(context.Background(), userID, id)
	if err != nil {
		t.Fatal(err)
	}
	return pageState{row.Title, row.Token, row.Visibility, row.Revision}
}

func (p pageState) same(t *testing.T, h *harness, userID, id string) {
	t.Helper()
	got := currentPage(t, h, userID, id)
	if got != p {
		t.Fatalf("page changed to %q %s revision %d", got.title, got.vis, got.rev)
	}
}

func grantSet(t *testing.T, h *harness, userID, pageID string) map[string]bool {
	t.Helper()
	rows, err := h.st.PageGrants(context.Background(), userID, pageID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, grant := range rows {
		out[grant.Kind+"/"+grant.GranteeID] = true
	}
	return out
}
