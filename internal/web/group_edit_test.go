package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func TestGroupControlsAsksAndReplies(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.105:4000"
	ctx := context.Background()
	const password = "correct-horse-1"
	owner, ownerSecret := h.account("alex-105@example.com", "Alex Rivera", password)
	outsider, outsiderSecret := h.account("blair-105@example.com", "Blair Quinn", password)
	hash, err := auth.Hash(password, h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	memberSecret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := h.st.CreateInvite(ctx, owner.ID, "pip-105@example.com", "Pip Lane")
	if err != nil {
		t.Fatal(err)
	}
	member, err := h.st.AcceptInvite(ctx, token, hash, memberSecret)
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	when := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return when
	})
	advance := func(d time.Duration) {
		clockMu.Lock()
		when = when.Add(d)
		clockMu.Unlock()
	}
	signIn := func(email, secret string) {
		t.Helper()
		advance(31 * time.Second)
		h.login(email, password, secret, ip, false)
	}
	sec, err := h.st.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	moss, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Moss", Title: "Research", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	atlasThread := agentThread(t, h, owner.ID, atlas.ID)
	if _, err := h.st.CreateMessage(ctx, owner.ID, atlasThread, "user", owner.ID, "keep this private chat"); err != nil {
		t.Fatal(err)
	}
	research, err := h.st.CreateSection(ctx, owner.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	signIn("alex-105@example.com", ownerSecret)

	code, body, _ := h.do(http.MethodGet, "/groups/new", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `href="/">Cancel</a>`) || !strings.Contains(body, "They can reply on their own.") || !strings.Contains(body, `name="section"`) || !strings.Contains(body, "Research") || strings.Contains(body, "A group needs at least two of your agents.") {
		t.Fatalf("new group %d", code)
	}
	before, err := h.st.ListGroups(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatal("cancel page created a group")
	}

	h.logout(ip)
	signIn("blair-105@example.com", outsiderSecret)
	code, body, _ = h.do(http.MethodGet, "/groups/new", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "A group needs at least two of your agents.") || !strings.Contains(body, `href="/">Cancel</a>`) {
		t.Fatalf("empty new group %d", code)
	}
	if rows, err := h.st.ListGroups(ctx, outsider.ID); err != nil || len(rows) != 0 {
		t.Fatal("empty cancel created a group")
	}

	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, _, _ = h.do(http.MethodPost, "/groups", h.form(url.Values{"title": {"Nope"}, "agent": {atlas.ID}, "section": {research.ID}}), ip, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("one agent %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/groups", h.form(url.Values{"title": {"Standup"}, "agent": {atlas.ID, pip.ID}, "section": {outsider.ID}}), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign section %d", code)
	}
	if rows, err := h.st.ListGroups(ctx, owner.ID); err != nil || len(rows) != 0 {
		t.Fatal("rejected create left a group")
	}
	code, _, hdr := h.do(http.MethodPost, "/groups", h.form(url.Values{"title": {"Standup"}, "agent": {atlas.ID, pip.ID}, "member": {member.ID}, "section": {research.ID}}), ip, nil)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/groups/") {
		t.Fatalf("create %d %s", code, hdr.Get("Location"))
	}
	groupID := strings.TrimPrefix(hdr.Get("Location"), "/groups/")
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Count(body, `/groups/`+groupID) != 1 || !strings.Contains(body, "Research") || !strings.Contains(body, "Group chat") {
		t.Fatalf("placed group %d", code)
	}
	advance(time.Second)
	code, _, hdr = h.do(http.MethodPost, "/groups", h.form(url.Values{"title": {"Planning"}, "agent": {atlas.ID, moss.ID}, "section": {"groups"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("second group %d", code)
	}
	planID := strings.TrimPrefix(hdr.Get("Location"), "/groups/")
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Index(body, "Planning") < 0 || strings.Index(body, "Standup") < 0 || strings.Index(body, "Planning") < strings.Index(body, "Standup") {
		t.Fatal("unpinned order")
	}
	advance(time.Second)
	if code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/section", h.form(url.Values{"section": {"groups"}}), ip, nil); code != http.StatusSeeOther {
		t.Fatalf("unplace %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Index(body, "Planning") < 0 || strings.Index(body, "Planning") > strings.Index(body, "Standup") || strings.Contains(body, ">Pinned</span>") {
		t.Fatal("groups list order")
	}
	advance(time.Second)
	code, _, hdr = h.do(http.MethodPost, "/groups/"+planID+"/pin", h.form(nil), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+planID+"?panel=group" {
		t.Fatalf("pin %d %s", code, hdr.Get("Location"))
	}
	advance(time.Second)
	if code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/pin", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatalf("pin standup %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Index(body, "Standup") < 0 || strings.Index(body, "Standup") > strings.Index(body, "Planning") || !strings.Contains(body, ">Pinned</span>") {
		t.Fatal("pinned order")
	}
	if code, _, _ = h.do(http.MethodPost, "/groups/"+planID+"/pin", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatalf("unpin %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `aria-label="Group panel"`) || !strings.Contains(body, `id="agent-panel"`) || !strings.Contains(body, "Agents here can reply.") || strings.Contains(body, "Mention an agent with @Name and that agent replies here.") || strings.Contains(body, `name="section"`) || strings.Contains(body, ">Unpin<") {
		t.Fatalf("group actions %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"?panel=group", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, ">Unpin<") || !strings.Contains(body, `name="section"`) || !strings.Contains(body, `name="agent"`) || !strings.Contains(body, `value="`+atlas.ID+`" checked`) || !strings.Contains(body, `href="/groups/`+groupID+`/delete"`) || strings.Contains(body, ">Edit<") {
		t.Fatalf("group panel %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"?panel=group", "", ip, map[string]string{"HX-Request": "true", "HX-Target": "agent-panel"})
	if code != http.StatusOK || strings.Contains(body, `id="composer"`) || !strings.Contains(body, `id="agent-panel"`) || !strings.Contains(body, `name="agent"`) || !strings.Contains(body, ">Unpin<") {
		t.Fatalf("group panel partial %d", code)
	}

	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK || strings.Count(body, `/groups/`+groupID) != 1 || strings.Contains(body, ">Pinned</span>") || strings.Contains(body, "/groups/"+planID) {
		t.Fatal("member sidebar followed the owner")
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"?panel=group", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, ">Pin<") || !strings.Contains(body, `name="section"`) || !strings.Contains(body, "In this group") || strings.Contains(body, `name="agent"`) || strings.Contains(body, `href="/groups/`+groupID+`/delete"`) || strings.Contains(body, ">Save<") {
		t.Fatalf("member panel %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"/edit", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member edit %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Stolen"}, "agent": {atlas.ID, pip.ID}}), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member save %d", code)
	}
	memberSec, err := h.st.CreateSection(ctx, member.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/section", h.form(url.Values{"section": {memberSec.ID}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("member place %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Count(body, `/groups/`+groupID) != 1 || !strings.Contains(body, "Research") {
		t.Fatal("member section")
	}
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/section", h.form(url.Values{"section": {research.ID}}), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("foreign place %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Count(body, `/groups/`+groupID) != 1 {
		t.Fatal("foreign place moved the group")
	}
	if code, _, _ = h.do(http.MethodPost, "/sections/"+sec.ID+"/fold", h.form(nil), ip, map[string]string{"Referer": "http://example.com/?list=1"}); code != http.StatusNotFound {
		t.Fatalf("member fold %d", code)
	}

	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if !strings.Contains(body, `data-name="Atlas"`) || strings.Contains(body, `aria-label="Expand Agents"`) {
		t.Fatal("owner agents folded by the member")
	}
	code, _, hdr = h.do(http.MethodPost, "/sections/"+sec.ID+"/fold", h.form(nil), ip, map[string]string{"Referer": "http://example.com/?list=1"})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/?list=1" {
		t.Fatalf("fold %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Contains(body, `data-name="Atlas"`) || !strings.Contains(body, `aria-label="Expand Agents"`) || strings.Count(body, `/groups/`+groupID) != 1 {
		t.Fatal("collapsed agents hid the wrong rows")
	}
	code, _, _ = h.do(http.MethodPost, "/sections/"+sec.ID+"/fold", "csrf=nope", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(mustBody(h, ip, "/?list=1"), `aria-label="Expand Agents"`) {
		t.Fatal("bad csrf expanded the section")
	}
	if code, _, _ = h.do(http.MethodPost, "/sections/groups/fold", h.form(nil), ip, map[string]string{"Referer": "http://example.com/?list=1"}); code != http.StatusSeeOther {
		t.Fatalf("fold groups %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Contains(body, "/groups/"+planID) || !strings.Contains(body, `aria-label="Expand Groups"`) || !strings.Contains(body, `href="/groups/new"`) {
		t.Fatal("groups collapse")
	}
	if code, _, _ = h.do(http.MethodPost, "/sections/groups/fold", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal("expand groups")
	}
	if code, _, _ = h.do(http.MethodPost, "/sections/"+sec.ID+"/fold", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal("expand agents")
	}

	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"/edit", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `value="Standup"`) || !strings.Contains(body, `value="`+atlas.ID+`" checked`) && !strings.Contains(body, `checked value="`+atlas.ID+`"`) {
		if code != http.StatusOK || !strings.Contains(body, atlas.ID) || !strings.Contains(body, "checked") || !strings.Contains(body, `href="/groups/`+groupID+`">Cancel</a>`) {
			t.Fatalf("edit page %d", code)
		}
	}
	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {""}, "agent": {atlas.ID, pip.ID}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Enter a title and choose at least two of your agents.") {
		t.Fatalf("empty title %d", code)
	}
	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Standup"}, "agent": {atlas.ID}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Enter a title and choose at least two of your agents.") {
		t.Fatalf("one agent save %d", code)
	}
	queued, err := h.st.CreateRun(ctx, owner.ID, groupID, atlas.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Busy"}, "agent": {atlas.ID, pip.ID}, "member": {member.ID}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Wait until the reply finishes.") {
		t.Fatalf("busy edit %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"/delete", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Wait until the reply finishes.") || strings.Contains(body, ">Delete group</button>") {
		t.Fatalf("busy delete page %d", code)
	}
	if _, err := h.st.FailRun(ctx, owner.ID, queued.ID, "stopped", 0); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetThread(ctx, owner.ID, groupID)
	if err != nil || got.Title != "Standup" {
		t.Fatal("busy save changed the title")
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Forum"}, "agent": {atlas.ID, moss.ID}, "member": {member.ID}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID+"?panel=group" {
		t.Fatalf("save %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	who := subText(body)
	if !strings.Contains(body, "Forum") || !strings.Contains(who, "Atlas") || !strings.Contains(who, "Moss") || strings.Contains(who, ", Pip,") || strings.Contains(who, "and Pip.") || strings.HasPrefix(who, "With Pip ") {
		t.Fatal("rename was not visible")
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Forum") || strings.Contains(body, ">Edit<") {
		t.Fatalf("member renamed %d", code)
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/edit", h.form(url.Values{"title": {"Forum"}, "agent": {atlas.ID, moss.ID}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("remove member %d", code)
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, _, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("removed member %d", code)
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "hello team")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("look post %d", code)
	}
	settleGroup(t, h, owner.ID, groupID)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if strings.Count(body, "has something to add.") != 2 || strings.Contains(body, "You said:") || strings.Contains(body, "[[group-look]]") {
		t.Fatalf("unmentioned replies (%d bytes)", len(body))
	}
	runs, err := h.st.ListThreadRuns(ctx, owner.ID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	looks := 0
	var shared string
	for _, row := range runs {
		if row.ID == queued.ID {
			continue
		}
		if row.AgentID == pip.ID {
			t.Fatal("removed agent replied")
		}
		looks++
		if row.AgentID == atlas.ID {
			shared = row.ID
		}
	}
	if looks != 2 || shared == "" {
		t.Fatalf("look runs %d", looks)
	}
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "quiet please")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal("quiet post")
	}
	settleGroup(t, h, owner.ID, groupID)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if adds := strings.Count(body, "has something to add."); strings.Contains(body, "PASS") || adds != 2 {
		t.Fatalf("silence posted pass=%t adds=%d", strings.Contains(body, "PASS"), adds)
	}
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "@Atlas once more")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal("mention after cap")
	}
	settleGroup(t, h, owner.ID, groupID)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if !strings.Contains(body, "You said: @Atlas once more") {
		t.Fatal("mention after the cap did not reply")
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, _, _ = h.do(http.MethodGet, "/runs/"+shared, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member run %d", code)
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, runBody, _ := h.do(http.MethodGet, "/runs/"+shared, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(runBody, shared+"\t") {
		t.Fatalf("owner run %d", code)
	}

	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Rename this group to Planning")), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID {
		t.Fatalf("rename ask %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if !strings.Contains(body, "Renamed to Planning.") || !strings.Contains(body, "Planning") {
		t.Fatal("prompted rename")
	}
	beforeRuns, err := h.st.ListThreadRuns(ctx, owner.ID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Rename this group to Planning and add Moss to this group")), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "ask=unclear") {
		t.Fatalf("two changes %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if !strings.Contains(body, "That did not change the group.") || !strings.Contains(body, "Planning") {
		t.Fatal("two changes changed the group")
	}
	afterRuns, err := h.st.ListThreadRuns(ctx, owner.ID, groupID)
	if err != nil || len(afterRuns) != len(beforeRuns) {
		t.Fatal("unclear ask started a run")
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Rename this group to Secret @Atlas")), ip, nil)
	if code != http.StatusSeeOther || strings.Contains(hdr.Get("Location"), "ask=") {
		t.Fatalf("mention ask %d %s", code, hdr.Get("Location"))
	}
	settleGroup(t, h, owner.ID, groupID)
	got, err = h.st.GetThread(ctx, owner.ID, groupID)
	if err != nil || got.Title != "Planning" {
		t.Fatal("mention ask renamed the group")
	}

	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Rename this group to Stolen")), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("removed ask %d", code)
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	if err := h.st.UpdateGroup(ctx, owner.ID, groupID, "Planning", []string{atlas.ID, moss.ID}, []string{member.ID}); err != nil {
		t.Fatal(err)
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Rename this group to Stolen")), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "ask=creator") {
		t.Fatalf("member rename %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if !strings.Contains(body, "Only the person who created this group can change that.") {
		t.Fatal("creator note missing")
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if strings.Contains(body, "Only the person who created this group can change that.") {
		t.Fatal("owner saw the member note")
	}
	groupsNamed, err := h.st.CreateSection(ctx, owner.ID, "Groups")
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Put this group in Groups")), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "ask=unclear") {
		t.Fatalf("ambiguous groups %d %s", code, hdr.Get("Location"))
	}
	if err := h.st.DeleteSection(ctx, owner.ID, groupsNamed.ID); err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Put this group in Research")), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "ask=placed") {
		t.Fatalf("place ask %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if !strings.Contains(body, "This group is in Research.") {
		t.Fatal("place note")
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if strings.Contains(body, "This group is in Research.") {
		t.Fatal("member saw the placement note")
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)

	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Delete this group")), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID+"/delete" {
		t.Fatalf("delete ask %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Delete Planning?") || !strings.Contains(body, "The chat is removed for everyone.") || !strings.Contains(body, "The agents are kept.") {
		t.Fatalf("confirm %d", code)
	}
	first := confirmValue(t, body)
	code, _, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusOK {
		t.Fatal("cancel left the group")
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/messages", h.form(mapForm("body", "Delete this group")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal("second delete ask")
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+groupID+"/delete", "", ip, nil)
	second := confirmValue(t, body)
	if second == first {
		t.Fatal("newer ask kept the old confirm")
	}
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/delete", h.form(url.Values{"confirm": {first}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/groups/"+groupID {
		t.Fatalf("stale confirm %d %s", code, hdr.Get("Location"))
	}
	if _, err := h.st.GetThread(ctx, owner.ID, groupID); err != nil {
		t.Fatal("stale confirm deleted the group")
	}
	code, body, _ = h.do(http.MethodPost, "/groups/"+groupID+"/delete", "csrf=nope&confirm="+second, ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("bad csrf %d", code)
	}
	if _, err := h.st.GetThread(ctx, owner.ID, groupID); err != nil {
		t.Fatal("bad csrf deleted the group")
	}
	h.logout(ip)
	signIn("blair-105@example.com", outsiderSecret)
	code, _, _ = h.do(http.MethodGet, "/groups/"+groupID+"/delete", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("outsider delete %d", code)
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, _, _ = h.do(http.MethodPost, "/groups/"+groupID+"/delete", h.form(url.Values{"confirm": {second}}), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("member delete %d", code)
	}
	h.logout(ip)
	signIn("alex-105@example.com", ownerSecret)
	code, _, hdr = h.do(http.MethodPost, "/groups/"+groupID+"/delete", h.form(url.Values{"confirm": {second}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("delete %d %s", code, hdr.Get("Location"))
	}
	code, _, _ = h.do(http.MethodGet, "/groups/"+groupID, "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("old url %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+atlas.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "keep this private chat") {
		t.Fatal("delete removed the agent chat")
	}
	h.logout(ip)
	signIn("pip-105@example.com", memberSecret)
	code, body, _ = h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if strings.Contains(body, groupID) || strings.Contains(body, "Planning") {
		t.Fatal("deleted group stayed in the member sidebar")
	}
}

func settleGroup(t *testing.T, h *harness, userID, threadID string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	prev := -1
	stable := 0
	for time.Now().Before(deadline) {
		h.waitRuns(userID, threadID)
		h.srv.runs.Wait(400 * time.Millisecond)
		runs, err := h.st.ListThreadRuns(context.Background(), userID, threadID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == prev {
			stable++
			if stable >= 2 {
				return
			}
		} else {
			stable = 0
			prev = len(runs)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("group runs did not settle")
}

func confirmValue(t *testing.T, body string) string {
	t.Helper()
	const key = `name="confirm" value="`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatal("confirm field missing")
	}
	rest := body[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 8 {
		t.Fatal("confirm field short")
	}
	return rest[:j]
}

func mustBody(h *harness, ip, path string) string {
	_, body, _ := h.do(http.MethodGet, path, "", ip, nil)
	return body
}
