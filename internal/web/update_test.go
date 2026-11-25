package web

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/update"
)

func TestOwnerSavesUpdateMode(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.50:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	_, secret := h.account("owner-update@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-update@example.com", "correct-horse-1", secret, ip, false)

	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `id="update"`) || !strings.Contains(body, "Up to date.") {
		t.Fatalf("settings %d update section missing", code)
	}
	if strings.Contains(body, "Update now") || strings.Contains(body, "sb-up") {
		t.Fatal("no release is waiting")
	}

	code, _, hdr := h.do(http.MethodPost, "/settings/update", h.form(url.Values{"mode": {"auto"}}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=update") {
		t.Fatalf("save %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings?notice=update", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Saved.") {
		t.Fatalf("notice %d", code)
	}
	if !strings.Contains(body, `name="mode" value="auto" checked`) {
		t.Fatal("auto was not selected")
	}
	got, err := h.st.InstanceSetting(context.Background(), update.ModeKey)
	if err != nil || got != update.ModeAuto {
		t.Fatalf("stored %q %v", got, err)
	}

	code, body, _ = h.do(http.MethodPost, "/settings/update", h.form(url.Values{"mode": {"later"}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Choose ask me, install updates") {
		t.Fatalf("bad mode %d", code)
	}
	got, err = h.st.InstanceSetting(context.Background(), update.ModeKey)
	if err != nil || got != update.ModeAuto {
		t.Fatalf("stored after bad mode %q %v", got, err)
	}
}

func TestMemberUpdateIsHidden(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.51:4000"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, false)
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	owner, secret := h.account("owner-hide@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-hide@example.com", "correct-horse-1", secret, ip, false)
	if err := h.st.SetInstanceSetting(ctx, update.ModeKey, update.ModePrompt); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Castbot v0.20261124.2 is ready.") || !strings.Contains(body, `class="banner ok sb-up"`) {
		t.Fatal("owner banner missing")
	}

	const password = "correct-horse-1"
	hash, err := auth.Hash(password, h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	memberSecret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := h.st.CreateInvite(ctx, owner.ID, "member-hide@example.com", "Pip Lane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AcceptInvite(ctx, token, hash, memberSecret); err != nil {
		t.Fatal(err)
	}
	h.logout(ip)
	h.login("member-hide@example.com", password, memberSecret, ip, false)

	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, `id="update"`) || strings.Contains(body, "Ask me") {
		t.Fatalf("member settings showed the update section (%d)", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, "is ready") || strings.Contains(body, "sb-up") {
		t.Fatal("member saw the banner")
	}
	for _, call := range []struct {
		method, path string
		form         bool
	}{
		{http.MethodGet, "/settings/update", false},
		{http.MethodPost, "/settings/update", true},
		{http.MethodPost, "/settings/update/apply", true},
	} {
		form := ""
		if call.form {
			form = h.form(url.Values{"mode": {update.ModeAuto}})
		}
		code, _, _ = h.do(call.method, call.path, form, ip, nil)
		if code != http.StatusNotFound {
			t.Fatalf("%s %s %d", call.method, call.path, code)
		}
	}
	got, err := h.st.InstanceSetting(ctx, update.ModeKey)
	if err != nil || got != update.ModePrompt {
		t.Fatalf("member changed the mode to %q (%v)", got, err)
	}
}

func TestUpdateModeCSRF(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.52:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	_, secret := h.account("owner-csrf@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-csrf@example.com", "correct-horse-1", secret, ip, false)
	if err := h.st.SetInstanceSetting(context.Background(), update.ModeKey, update.ModePrompt); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/settings/update", "csrf=nope&mode=auto", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d", code)
	}
	got, err := h.st.InstanceSetting(context.Background(), update.ModeKey)
	if err != nil || got != update.ModePrompt {
		t.Fatalf("mode changed to %q (%v)", got, err)
	}
}

func TestDevUpdateCopy(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.53:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, false)
	h.srv.SetUpdate("dev", "unknown", "", path)
	_, secret := h.account("owner-dev@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-dev@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "This copy was built from a checkout.") {
		t.Fatalf("dev copy %d", code)
	}
	section := updateSection(t, body)
	if strings.Contains(section, "Update now") || strings.Contains(section, "Up to date.") || strings.Contains(section, "v0.20261124.2") {
		t.Fatal("a checkout offered an update")
	}
	code, _, _ = h.do(http.MethodPost, "/settings/update/apply", h.form(nil), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("dev apply %d", code)
	}
}

func TestUpdateModePinnedByEnv(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.54:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, false)
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), update.ModeOff, path)
	_, secret := h.account("owner-pin@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-pin@example.com", "correct-horse-1", secret, ip, false)
	if err := h.st.SetInstanceSetting(context.Background(), update.ModeKey, update.ModeAuto); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	section := updateSection(t, body)
	if !strings.Contains(section, `name="mode" value="off" checked disabled`) || !strings.Contains(section, "The host file chooses this.") {
		t.Fatal("env pin was editable")
	}
	if strings.Contains(section, ">Save</button>") {
		t.Fatal("pinned form had a save button")
	}
	if strings.Contains(section, "Update now") || strings.Contains(section, "Up to date.") || strings.Contains(section, "v0.20261124.2") {
		t.Fatal("off mode showed a release")
	}
	code, _, hdr := h.do(http.MethodPost, "/settings/update", h.form(url.Values{"mode": {update.ModeAuto}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings#update" {
		t.Fatalf("pinned post %d %s", code, hdr.Get("Location"))
	}
	got, err := h.st.InstanceSetting(context.Background(), update.ModeKey)
	if err != nil || got != update.ModeAuto {
		t.Fatalf("pinned post wrote %q (%v)", got, err)
	}
}

func TestUpdateWhileStreamingDoesNotApply(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.55:4000"
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, true)
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	called := 0
	h.srv.SetUpdateApply(func(context.Context) error {
		called++
		return nil
	})
	owner, secret := h.account("owner-busy@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-busy@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.st.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, owner.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/settings/update/apply", h.form(nil), ip, nil)
	if code != http.StatusConflict {
		t.Fatalf("busy %d", code)
	}
	if !strings.Contains(body, "Try again when the agent is idle.") || !strings.Contains(body, "Update now") {
		t.Fatal("busy copy missing")
	}
	if strings.Contains(body, "restarting") {
		t.Fatal("busy post claimed a restart")
	}
	if called != 0 {
		t.Fatalf("apply called %d", called)
	}
}

func TestUpdateApplyIdleDoesNotRestart(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.56:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, true)
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	_, secret := h.account("owner-idle@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-idle@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Update now") || !strings.Contains(body, "Release notes") {
		t.Fatal("ready copy missing")
	}
	if !strings.Contains(body, "Agents already running keep their current machine until Replace machine.") {
		t.Fatal("image move copy missing")
	}
	if strings.Contains(body, "GitHub") {
		t.Fatal("visible product name")
	}
	code, body, _ = h.do(http.MethodPost, "/settings/update/apply", h.form(nil), ip, nil)
	if code != http.StatusOK {
		t.Fatalf("idle %d", code)
	}
	if strings.Contains(body, "restarting") {
		t.Fatal("idle post claimed a restart")
	}
	if !strings.Contains(body, "Update now") {
		t.Fatal("button left after a dry run")
	}
}

func TestUpdateApplyHookRunsWhenIdle(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.57:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	writeUpdateCache(t, path, true, false)
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	called := 0
	h.srv.SetUpdateApply(func(context.Context) error {
		called++
		return nil
	})
	_, secret := h.account("owner-hook@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-hook@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodPost, "/settings/update/apply", h.form(nil), ip, nil)
	if code != http.StatusOK || called != 1 {
		t.Fatalf("hook %d called %d", code, called)
	}
	if strings.Contains(body, "restarting") {
		t.Fatal("hook success claimed a restart")
	}
}

func TestUpdateImageBlockedCopy(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.58:4000"
	path := filepath.Join(t.TempDir(), "update.json")
	if err := update.WriteCache(path, update.Cache{
		ImageBlocked: true,
		Version:      "0.20261124.2",
		Commit:       strings.Repeat("cd", 20),
		Notes:        "https://github.com/scott/castbot/releases/tag/v0.20261124.2",
		CheckedAt:    "2026-11-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.SetUpdate("0.20261124.1", strings.Repeat("ab", 20), "", path)
	_, secret := h.account("owner-block@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-block@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "The machine image has to be published for this release before the update can run.") {
		t.Fatalf("blocked copy %d", code)
	}
	if strings.Contains(body, "Update now") {
		t.Fatal("blocked release offered a button")
	}
	code, _, _ = h.do(http.MethodPost, "/settings/update/apply", h.form(nil), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("blocked apply %d", code)
	}
}

func TestUpdateSectionStaysOffUntilConfigured(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.59:4000"
	_, secret := h.account("owner-off@example.com", "Alex Rivera", "correct-horse-1")
	h.login("owner-off@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, `id="update"`) {
		t.Fatalf("unconfigured settings showed update (%d)", code)
	}
}

func updateSection(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `id="update"`)
	j := strings.Index(body, `id="people"`)
	if i < 0 || j < i {
		t.Fatal("update section missing")
	}
	return body[i:j]
}

func writeUpdateCache(t *testing.T, path string, available, moves bool) {
	t.Helper()
	if err := update.WriteCache(path, update.Cache{
		Available:  available,
		Version:    "0.20261124.2",
		Commit:     strings.Repeat("cd", 20),
		Notes:      "https://github.com/scott/castbot/releases/tag/v0.20261124.2",
		ImageMoves: moves,
		CheckedAt:  "2026-11-24T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}
