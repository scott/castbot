package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestAgentJobAndDelegateForm(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-form-key")
	ip := "203.0.113.194:9"
	h := newHarness(t)
	user, secret := h.account("delegate@example.com", "Owner", "correct-horse-1")
	h.login("delegate@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 || strings.Contains(body, "\u2014") {
		t.Fatalf("form %d", code)
	}
	for _, needle := range []string{
		"Can delegate to other agents",
		"This agent can message the other agents on this account and ask them to do part of a job.",
		"Other agents see this when they pick someone to ask.",
		`name="can_delegate" value="1" aria-label="Can delegate to other agents"`,
		`name="job"`,
		`aria-label="Job"`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
	if strings.Contains(body, `name="can_delegate" value="1" checked`) || strings.Count(body, `name="job"`) != 1 || strings.Count(body, `name="can_delegate"`) != 1 {
		t.Fatal("blank form")
	}

	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."}, "job": {"Writes the brief"},
		"can_delegate": {"1"},
	}
	code, body, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/agents/") {
		t.Fatalf("create %d %s", code, snippet(body))
	}
	agent := onlyAgent(t, h, user.ID)
	if agent.Description != "Writes the brief" || !agent.CanDelegate {
		t.Fatalf("stored %+v", agent)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != 200 || !strings.Contains(body, `name="can_delegate" value="1" checked`) || !strings.Contains(body, ">Writes the brief</textarea>") {
		t.Fatalf("edit %d %s", code, snippet(body))
	}

	fields["job"] = []string{strings.Repeat("a", 161)}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Use a job up to 160 characters.") {
		t.Fatalf("long job %d %s", code, snippet(body))
	}
	got, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil || got.Description != "Writes the brief" || !got.CanDelegate {
		t.Fatalf("long job wrote %+v %v", got, err)
	}

	delete(fields, "can_delegate")
	fields["job"] = []string{"Checks the docs"}
	code, body, hdr = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") == "" {
		t.Fatalf("save %d %s", code, snippet(body))
	}
	got, err = h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil || got.Description != "Checks the docs" || got.CanDelegate {
		t.Fatalf("saved %+v %v", got, err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != 200 || strings.Contains(body, `name="can_delegate" value="1" checked`) || !strings.Contains(body, ">Checks the docs</textarea>") {
		t.Fatalf("edit off %d", code)
	}
	code, roster, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/roster", "", ip, nil)
	if code != 200 || !strings.Contains(roster, `"description":"Checks the docs"`) {
		t.Fatalf("roster %d %s", code, roster)
	}

	_, bSecret := h.account("delegate-b@example.com", "User B", "correct-horse-1")
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	bip := "203.0.113.195:9"
	h.login("delegate-b@example.com", "correct-horse-1", bSecret, bip, false)
	fields["job"] = []string{"Stolen job"}
	fields["can_delegate"] = []string{"1"}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", bip, fields, nil)
	if code != http.StatusNotFound {
		t.Fatalf("B edit %d %s", code, snippet(body))
	}
	got, err = h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil || got.Description != "Checks the docs" || got.CanDelegate {
		t.Fatalf("B changed the row %+v %v", got, err)
	}
}

func TestRosterPrefaceWhenDelegateOn(t *testing.T) {
	client := &coordClient{}
	h := newHarnessWith(t, client)
	ip := "203.0.113.196:9"
	user, chief := delegatePair(t, h, ip, "gate-on@example.com", true)
	ctx := context.Background()
	if _, err := h.st.PutBundle(ctx, user.ID, chief.ID, "1", "name: Chief\n", "Be concise.\n"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/agents/"+chief.ID+"/messages", h.form(url.Values{"body": {"Please split this job."}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("post %d", code)
	}
	send := waitSend(t, client, func(send runtime.Send) bool {
		return strings.Contains(send.Text, "Agents you can ask") && strings.Contains(send.Text, "Writes the brief")
	})
	if !strings.Contains(send.Text, "Please split this job.") || strings.Contains(send.Text, "coordinate") {
		t.Fatalf("preface\n%s", send.Text)
	}
	h.srv.runs.Wait(2 * time.Second)
}

func TestRosterPrefaceWhenDelegateOff(t *testing.T) {
	client := &coordClient{}
	h := newHarnessWith(t, client)
	ip := "203.0.113.197:9"
	user, chief := delegatePair(t, h, ip, "gate-off@example.com", false)
	ctx := context.Background()
	if _, err := h.st.PutBundle(ctx, user.ID, chief.ID, "1", "name: Chief\n", "You coordinate the other agents.\n"); err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/agents/"+chief.ID+"/messages", h.form(url.Values{"body": {"Please split this job."}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("post %d", code)
	}
	send := waitSend(t, client, func(send runtime.Send) bool {
		return strings.Contains(send.Text, "Please split this job.")
	})
	if strings.Contains(send.Text, "Agents you can ask") {
		t.Fatalf("preface leaked\n%s", send.Text)
	}
	client.mu.Lock()
	sends := append([]runtime.Send(nil), client.sends...)
	client.mu.Unlock()
	for _, other := range sends {
		if strings.Contains(other.Text, "Agents you can ask") {
			t.Fatalf("preface leaked\n%s", other.Text)
		}
	}
	h.srv.runs.Wait(2 * time.Second)
}

func TestRosterNoteCopy(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{"Ada"}, "Can ask Ada."},
		{[]string{"Ada", "Bea"}, "Can ask Ada and Bea."},
		{[]string{"Ada", "Bea", "Cam"}, "Can ask Ada, Bea, and Cam."},
	}
	for _, tc := range cases {
		if got := rosterNote(tc.names); got != tc.want {
			t.Fatalf("%v: %q", tc.names, got)
		}
	}
}

func TestAgentPageRosterNote(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.198:9"
	user, chief, _, _ := talkCrew(t, h, ip)
	code, page, _ := h.do(http.MethodGet, "/agents/"+chief.ID, "", ip, nil)
	if code != 200 || strings.Contains(page, "Can ask") || strings.Contains(page, "roster-note") {
		t.Fatalf("off %d", code)
	}
	if err := h.st.SaveAgentFields(context.Background(), user.ID, chief.ID, store.AgentInput{
		SectionID: chief.SectionID, Name: chief.Name, Title: chief.Title, Description: chief.Description,
		Model: chief.Model, Color: chief.Color, CanDelegate: true,
	}); err != nil {
		t.Fatal(err)
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+chief.ID, "", ip, nil)
	if code != 200 || !strings.Contains(page, `<p class="roster-note">Can ask Ada, Bea, and Cam.</p>`) || strings.Contains(page, "\u2014") {
		t.Fatalf("on %d", code)
	}
}

func delegatePair(t *testing.T, h *harness, ip, email string, delegate bool) (store.User, store.Agent) {
	t.Helper()
	user, secret := h.account(email, "Owner", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
	ctx := context.Background()
	sec, err := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	chief, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Chief", Title: "Lead", Description: "Writes the plan",
		Status: "ready", CanDelegate: delegate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Ada", Title: "Writer", Description: "Writes the brief", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	return user, chief
}

func waitSend(t *testing.T, c *coordClient, ok func(runtime.Send) bool) runtime.Send {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		sends := append([]runtime.Send(nil), c.sends...)
		c.mu.Unlock()
		for _, send := range sends {
			if ok(send) {
				return send
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.mu.Lock()
	n := len(c.sends)
	c.mu.Unlock()
	t.Fatalf("timeout after %d sends", n)
	return runtime.Send{}
}
