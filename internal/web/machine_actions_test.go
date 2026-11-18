package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/store"
)

func TestMachineActionRows(t *testing.T) {
	ip := "203.0.113.73:9"
	h, _, agent, _ := runningRig(t, ip, "machine-actions@example.com")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 {
		t.Fatalf("panel %d", code)
	}
	for _, line := range []string{
		`<span class="t1">Pause</span>`,
		"Stops the machine until you wake it. A chat message wakes it too.",
		`<span class="t1">Refresh</span>`,
		"Checks the machine with the cloud API. This does not wake it.",
		`id="replace-open"`,
		`<span class="t1">Replace machine</span>`,
		"Starts a new machine on the current runtime and copies memory and workspace files. The agent and its chats stay.",
		`class="macts"`,
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("running panel missing %q", line)
		}
	}
	if strings.Contains(body, `<span class="t1">Wake</span>`) || strings.Contains(body, `class="acts racts"`) {
		t.Fatal("running panel still uses the old button row or offers wake")
	}

	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/pause", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("pause %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, `<span class="t1">Wake</span>`) || !strings.Contains(body, "Starts the paused machine so the agent can work. A chat message wakes it too.") || strings.Contains(body, `<span class="t1">Pause</span>`) {
		t.Fatal("paused panel did not switch to wake")
	}

	draft, _, err := h.st.CreateAgent(context.Background(), agent.UserID, store.AgentInput{
		SectionID: agent.SectionID, Name: "Sprig", Title: "No machine", Status: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+draft.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, `<span class="t1">Start machine</span>`) || !strings.Contains(body, "Starts a machine for this agent so you can chat.") {
		t.Fatalf("start row missing (%d)", code)
	}
}
