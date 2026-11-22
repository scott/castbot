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

func TestMembersInvitesAndGroupChat(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.50:4000"
	ctx := context.Background()
	const password = "correct-horse-1"
	owner, ownerSecret := h.account("alex-s13@example.com", "Alex Rivera", password)
	blair, blairSecret := h.account("blair-s13@example.com", "Blair Quinn", password)
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	signIn := func(email, secret string) {
		t.Helper()
		clockMu.Lock()
		clock = clock.Add(31 * time.Second)
		clockMu.Unlock()
		h.login(email, password, secret, ip, false)
	}
	signIn("alex-s13@example.com", ownerSecret)

	code, body, _ := h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "not-an-email", "name", "Pip")), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Enter a name and an email address.") {
		t.Fatalf("bad invite %d (%d bytes)", code, len(body))
	}
	code, body, _ = h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "blair-s13@example.com", "name", "Blair")), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "That email already has an account.") || strings.Contains(body, blair.ID) {
		t.Fatalf("existing email %d (%d bytes)", code, len(body))
	}

	code, body, _ = h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "pip-s13@example.com", "name", "Pip Lane")), ip, nil)
	if code != http.StatusOK || strings.Count(body, "Copy this invite link now. It is shown once.") != 1 {
		t.Fatalf("create invite %d (%d bytes)", code, len(body))
	}
	token := oneInviteToken(t, body)
	code, body, _ = h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "pip-s13@example.com", "name", "Pip Lane")), ip, nil)
	if code != http.StatusOK {
		t.Fatalf("refresh invite %d", code)
	}
	token2 := oneInviteToken(t, body)
	if token2 == token {
		t.Fatal("refresh kept the same link")
	}
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, `aria-label="Invite link"`) || strings.Contains(body, token) || strings.Contains(body, token2) {
		t.Fatal("invite link was shown again")
	}
	if !strings.Contains(body, "Pip Lane") || !strings.Contains(body, "Invite pending") || !strings.Contains(body, "Expires in 7 days") {
		t.Fatalf("pending row missing (%d bytes)", len(body))
	}
	if !strings.Contains(body, "0 of 4") || !strings.Contains(body, "0 B of 2 GB") || !strings.Contains(body, "Yours only") {
		t.Fatalf("owner quota missing (%d bytes)", len(body))
	}

	h.logout(ip)
	code, _, _ = h.do(http.MethodGet, "/invite/"+token, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("old link %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/invite/"+token2, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Join Personal") || !strings.Contains(body, "Alex Rivera invited Pip Lane") || !strings.Contains(body, `action="/invite/`+token2+`"`) {
		t.Fatalf("accept page %d (%d bytes)", code, len(body))
	}
	code, body, _ = h.do(http.MethodPost, "/invite/"+token2, h.form(mapForm("password", password, "again", "correct-horse-2")), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Use 12 or more characters, and enter the password twice.") || !strings.Contains(body, `action="/invite/`+token2+`"`) {
		t.Fatalf("password mismatch %d (%d bytes)", code, len(body))
	}
	code, body, hdr := h.do(http.MethodPost, "/invite/"+token2, h.form(mapForm("password", password, "again", password)), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" || strings.Contains(body, "otpauth://") || strings.Contains(body, "Authenticator secret") {
		t.Fatalf("accept %d %s", code, hdr.Get("Location"))
	}
	code, home, hdr := h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/new-agent" {
		t.Fatalf("accepted home %d %s", code, hdr.Get("Location"))
	}
	code, home, _ = h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(home, "Pip Lane") || strings.Contains(home, "Create your first agent") || strings.Contains(home, "Pick an agent") || strings.Contains(home, "otpauth://") || strings.Contains(home, token2) {
		t.Fatal("accepted session did not open the new-agent screen")
	}
	code, spent, _ := h.do(http.MethodGet, "/invite/"+token2, "", ip, nil)
	if code != http.StatusNotFound || strings.Contains(spent, "otpauth://") {
		t.Fatal("spent invite still opens")
	}
	code, _, _ = h.do(http.MethodPost, "/invite/"+token2, h.form(mapForm("password", password, "again", password)), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("second accept %d", code)
	}

	var member store.User
	people, err := h.st.ListMembers(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, person := range people {
		if person.Email == "pip-s13@example.com" {
			member = person
		}
	}
	if member.ID == "" || member.Role != "member" || member.WorkspaceID != owner.WorkspaceID {
		t.Fatalf("member role %s workspace %s", member.Role, member.WorkspaceID)
	}

	h.logout(ip)
	h.loginPassword("pip-s13@example.com", password, ip)
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Member of Personal workspace") || !strings.Contains(body, "The owner invites people to this workspace.") || strings.Contains(body, "Create invite") || strings.Contains(body, "otpauth://") || strings.Contains(body, "8 recovery codes") {
		t.Fatal("member settings leaked the invite form or the secret")
	}
	if !strings.Contains(body, "0 of 4") || !strings.Contains(body, "0 B of 2 GB") || !strings.Contains(body, "Pip Lane (you)") || !strings.Contains(body, ">Owner<") {
		t.Fatalf("member people list %d bytes", len(body))
	}
	code, _, _ = h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "new-s13@example.com", "name", "New")), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member invite %d", code)
	}

	h.logout(ip)
	signIn("alex-s13@example.com", ownerSecret)
	sec, err := h.st.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, atlasThread, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, atlasThread.ID, "user", owner.ID, "owner private note 13"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, owner.ID, atlas.ID, "vm-s13", "example.invalid/castbot-runtime:s13"); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+atlas.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "owner private note 13") || dmShowsAuthor(body, "owner private note 13") {
		t.Fatal("dm user bubble changed")
	}
	code, _, hdr = h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/agents/"+atlas.ID {
		t.Fatalf("owner home %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "1 of 4") || !strings.Contains(body, `aria-label="Collapse Groups"`) || strings.Contains(body, `action="/groups"`) || strings.Contains(body, ">New</button>") {
		t.Fatalf("owner machines %d bytes", len(body))
	}
	groupID := startGroupID(t, h, ip)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"?panel=group", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `name="agent" value="`+atlas.ID) || !strings.Contains(body, `name="agent" value="`+pip.ID) || !strings.Contains(body, `name="member" value="`+member.ID) || !strings.Contains(body, "Checked people can read this chat") || !strings.Contains(body, "A group needs at least two of your agents.") {
		t.Fatalf("new group %d (%d bytes)", code, len(body))
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Standup"}, "agent": {atlas.ID, pip.ID}, "member": {member.ID}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID+"?panel=group" {
		t.Fatalf("create group %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "late-s13@example.com", "name", "Late Joiner")), ip, nil)
	if code != http.StatusOK {
		t.Fatalf("late invite %d", code)
	}
	lateID := inviteRowID(t, body)
	if strings.Contains(body, "late-s13") && strings.Contains(lateID, "late") {
		t.Fatal("invite id parsed the email")
	}

	h.logout(ip)
	signIn("blair-s13@example.com", blairSecret)
	for _, path := range []string{"/groups/" + groupID, "/groups/" + groupID + "/feed", "/agents/" + atlas.ID} {
		code, _, _ = h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound {
			t.Fatalf("blair %s %d", path, code)
		}
	}
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "hello from blair")), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("blair post %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/invites/"+lateID+"/revoke", h.form(nil), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("blair revoke %d", code)
	}
	code, body, hdr = h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/new-agent" || strings.Contains(body, "Standup") || strings.Contains(body, "owner private note 13") || strings.Contains(body, groupID) {
		t.Fatalf("blair home %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if strings.Contains(body, "Standup") || strings.Contains(body, "owner private note 13") || strings.Contains(body, groupID) || strings.Contains(body, "/agents/"+atlas.ID) {
		t.Fatal("blair new-agent saw the shared group")
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, "Standup") || strings.Contains(body, "owner private note 13") || strings.Contains(body, groupID) || strings.Contains(body, "Pick an agent") {
		t.Fatal("blair list saw the shared group")
	}

	h.logout(ip)
	signIn("alex-s13@example.com", ownerSecret)
	code, _, hdr = h.do(http.MethodPost, "/settings/invites/"+lateID+"/revoke", h.form(nil), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings#people" {
		t.Fatalf("revoke %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if strings.Contains(body, "late-s13@example.com") || strings.Contains(body, "Late Joiner") {
		t.Fatal("revoked invite stayed on the page")
	}

	h.logout(ip)
	h.loginPassword("pip-s13@example.com", password, ip)
	code, body, hdr = h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/new-agent" || strings.Contains(body, "Standup") || strings.Contains(body, "owner private note 13") {
		t.Fatalf("member home %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Standup") || strings.Contains(body, "Pick an agent") || strings.Contains(body, "Create your first agent") || strings.Contains(body, `action="/groups"`) || strings.Contains(body, "/agents/"+atlas.ID) || strings.Contains(body, "owner private note 13") {
		t.Fatal("member list did not show the shared group")
	}
	code, body, _ = h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Standup") || strings.Contains(body, "/agents/"+atlas.ID) || strings.Contains(body, "owner private note 13") {
		t.Fatal("member new-agent dropped the group or showed the owner's agent")
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+atlas.ID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member agent %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Standup") || !strings.Contains(body, "With ") || !strings.Contains(body, "Atlas") || !strings.Contains(body, "Pip Lane") || !strings.Contains(body, "Alex Rivera") || !strings.Contains(body, `placeholder="Message the group"`) || strings.Contains(body, "owner private note 13") {
		t.Fatalf("group page %d (%d bytes)", code, len(body))
	}
	queued, err := h.st.CreateRun(ctx, owner.ID, groupID, atlas.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, `/groups/`+groupID+`/feed`) || !strings.Contains(body, `class="sysline">Replying.`) {
		t.Fatal("open group did not poll")
	}
	if _, err := h.st.FailRun(ctx, owner.ID, queued.ID, "stopped", 0); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if strings.Contains(body, `every 2s`) || strings.Contains(body, `Replying.`) {
		t.Fatal("idle group kept polling")
	}

	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "   ")), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusBadRequest || !strings.Contains(body, "Enter a message.") {
		t.Fatalf("empty post %d (%d bytes)", code, len(body))
	}
	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "hello from pip")), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || strings.Count(body, "hello from pip") != 1 || !strings.Contains(body, `id="group-feed"`) || strings.Contains(body, `class="comp"`) || strings.Contains(body, "You said:") || !strings.Contains(body, `class="who">Pip Lane`) {
		t.Fatalf("hx post %d (%d bytes)", code, len(body))
	}
	runs, err := h.st.ListThreadRuns(ctx, owner.ID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("mentionless post started %d runs", len(runs))
	}
	for _, row := range runs {
		if row.ID == queued.ID {
			continue
		}
		if row.UserID != owner.ID || (row.AgentID != atlas.ID && row.AgentID != pip.ID) {
			t.Fatalf("look run %#v", row)
		}
	}

	mention := "@Atlas and @Pip please look"
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", mention)), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID {
		t.Fatalf("mention post %d %s", code, hdr.Get("Location"))
	}
	h.waitRuns(owner.ID, groupID)
	body = h.poll(ip, "/groups/"+groupID, "You said:")
	if strings.Count(body, "You said:") != 2 || strings.Count(body, mention) != 3 {
		t.Fatalf("replies you said %d mention %d (%d bytes)", strings.Count(body, "You said:"), strings.Count(body, mention), len(body))
	}
	if !strings.Contains(body, `class="who">Atlas`) || !strings.Contains(body, `class="who">Pip</div>`) || !strings.Contains(body, `class="who">Pip Lane`) || strings.Contains(body, `every 2s`) || strings.Contains(body, "owner private note 13") {
		t.Fatal("group replies missing an agent name")
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "@Blair please")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("unmatched mention %d", code)
	}
	h.waitRuns(owner.ID, groupID)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || strings.Count(body, "You said:") != 2 || strings.Contains(subText(body), "Blair") {
		t.Fatal("an unmatched name started a reply")
	}
	runs, err = h.st.ListThreadRuns(ctx, owner.ID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	doneAtlas, donePip := false, false
	var sharedRun string
	for _, row := range runs {
		if row.Status != "done" {
			continue
		}
		if row.UserID != owner.ID {
			t.Fatal("group run was not owned by the thread owner")
		}
		if row.AgentID == atlas.ID {
			doneAtlas = true
			sharedRun = row.ID
		}
		if row.AgentID == pip.ID {
			donePip = true
		}
	}
	if !doneAtlas || !donePip || sharedRun == "" {
		t.Fatal("both agents did not finish")
	}
	code, _, _ = h.do(http.MethodGet, "/runs/"+sharedRun, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member run %d", code)
	}

	memberSec, err := h.st.SystemSection(ctx, member.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	notebook, _, err := h.st.CreateAgent(ctx, member.ID, store.AgentInput{SectionID: memberSec.ID, Name: "Notebook", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, member.ID, notebook.ID, "vm-pip", "example.invalid/castbot-runtime:s13"); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "1 of 4") || strings.Contains(body, "2 of 4") {
		t.Fatalf("member machines %d bytes", len(body))
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if strings.Contains(subText(body), "Notebook") || strings.Contains(body, `class="who">Notebook`) {
		t.Fatal("group page named an agent outside the group")
	}

	h.logout(ip)
	signIn("alex-s13@example.com", ownerSecret)
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "1 of 4") || strings.Contains(body, "2 of 4") || !strings.Contains(body, "Owner of Personal workspace") {
		t.Fatalf("owner machines after member vm %d bytes", len(body))
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+notebook.ID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("owner notebook %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, mention) || strings.Contains(body, "owner private note 13") || strings.Contains(body, "Notebook") {
		t.Fatal("owner group view diverged")
	}
	code, runBody, _ := h.do(http.MethodGet, "/runs/"+sharedRun, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(runBody, sharedRun+"\tdone\n") {
		t.Fatalf("owner run %d (%d bytes)", code, len(runBody))
	}
}

func TestExpiredInviteIsNotFound(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.51:4000"
	const password = "correct-horse-1"
	_, secret := h.account("alex-expire@example.com", "Alex Rivera", password)
	start := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return start })
	h.login("alex-expire@example.com", password, secret, ip, false)
	code, body, _ := h.do(http.MethodPost, "/settings/invites", h.form(mapForm("email", "late-only@example.com", "name", "Late")), ip, nil)
	if code != http.StatusOK {
		t.Fatalf("invite %d", code)
	}
	token := oneInviteToken(t, body)
	h.logout(ip)
	h.st.SetNow(func() time.Time { return start.Add(store.InviteWindow + time.Minute) })
	code, _, _ = h.do(http.MethodGet, "/invite/"+token, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("expired %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/invite/cbi_not-a-real-token", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown %d", code)
	}
}

func (h *harness) logout(ip string) {
	h.t.Helper()
	h.do(http.MethodPost, "/logout", h.form(nil), ip, nil)
}

func mapForm(pairs ...string) url.Values {
	vals := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		vals.Set(pairs[i], pairs[i+1])
	}
	return vals
}

func oneInviteToken(t *testing.T, body string) string {
	t.Helper()
	const key = "/invite/cbi_"
	if strings.Count(body, key) != 1 {
		t.Fatalf("invite link count %d", strings.Count(body, key))
	}
	i := strings.Index(body, key)
	rest := body[i+len("/invite/"):]
	j := strings.IndexAny(rest, `"'<& `)
	if j < 8 || !strings.HasPrefix(rest[:j], "cbi_") {
		t.Fatal("invite link was not a token")
	}
	return rest[:j]
}

func labeledValue(t *testing.T, body, label string) string {
	t.Helper()
	needle := `aria-label="` + label + `"`
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("missing %s", label)
	}
	k := strings.LastIndex(body[:i], `value="`)
	if k < 0 {
		t.Fatalf("missing %s value", label)
	}
	rest := body[k+len(`value="`):]
	j := strings.Index(rest, `"`)
	if j < 8 {
		t.Fatalf("empty %s", label)
	}
	return rest[:j]
}

func inviteRowID(t *testing.T, body string) string {
	t.Helper()
	const key = `action="/settings/invites/`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatal("revoke form missing")
	}
	rest := body[i+len(key):]
	j := strings.Index(rest, `/revoke"`)
	if j < 8 {
		t.Fatal("revoke form unclosed")
	}
	return rest[:j]
}

func subText(body string) string {
	const start = `<p class="sub">`
	i := strings.Index(body, start)
	if i < 0 {
		return ""
	}
	rest := body[i+len(start):]
	j := strings.Index(rest, "</p>")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func dmShowsAuthor(body, text string) bool {
	i := strings.Index(body, `class="m-user`)
	j := strings.Index(body, text)
	if i < 0 || j < i {
		return true
	}
	return strings.Contains(body[i:j], `class="who"`)
}
