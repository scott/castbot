package web

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func seedCaller(t *testing.T, h *harness, user store.User, name string) store.Agent {
	t.Helper()
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Description: "Helps with the account",
		Model: defaultModelID(), Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: "+name+"\n", "Be brief.\n"); err != nil {
		t.Fatal(err)
	}
	return agent
}

func createPayload(description, name, userID string) string {
	raw, err := json.Marshal(map[string]any{
		"name": agentskill.ToolCreate,
		"arguments": map[string]string{
			"description": description,
			"name":        name,
			"user_id":     userID,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestMachineCreatesAgentForOwner(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("create-owner@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	other, _ := h.account("create-other@example.com", "Bea", "correct-horse-1")
	addModelKey(t, h, other.ID)
	caller := seedCaller(t, h, user, "Ada")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.240:9"
	desc := "Sort the support inbox and draft replies for me to check."

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", createPayload(desc, "Quill", other.ID))
	if code != http.StatusNotFound || strings.Contains(body, "Quill") {
		t.Fatalf("other token %d %s", code, body)
	}
	if len(agentRows(t, h, user.ID)) != 1 || len(agentRows(t, h, other.ID)) != 1 {
		t.Fatal("other token created an agent")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", createPayload(desc, "Quill", other.ID))
	if code != http.StatusOK || !strings.Contains(body, "Created Quill") || strings.Contains(body, `"error":true`) {
		t.Fatalf("create %d %s", code, body)
	}
	made := agentNamed(t, h, user.ID, "Quill")
	if made.UserID != user.ID || made.Description == "" {
		t.Fatalf("created %+v", made)
	}
	if _, err := h.st.GetAgent(context.Background(), other.ID, made.ID); err == nil {
		t.Fatal("other user can read the new agent")
	}
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, made.ID)
	if err != nil || !strings.Contains(bundle.YAML, agentskill.ToolCreate) || bundle.Extras[agentskill.SkillFile] == "" {
		t.Fatal("new agent bundle is missing the skill")
	}
	if strings.Contains(bundle.Instructions, agentskill.Heading) {
		t.Fatal("stored instructions include the served guide")
	}

	code, listed := machinePost(t, h, ip, token, caller.ID, "/agents", `{"name":"list_agents","arguments":{}}`)
	if code != http.StatusOK || !strings.Contains(listed, "Quill") || !strings.Contains(listed, made.ID) || strings.Contains(listed, "Bea") {
		t.Fatalf("list %d %s", code, listed)
	}
	code, listed = machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", `{"name":"list_agents","arguments":{}}`)
	if code != http.StatusOK || strings.Contains(listed, "Quill") || strings.Contains(listed, made.ID) || !strings.Contains(listed, "Bea") {
		t.Fatalf("other list %d %s", code, listed)
	}
}

func TestMachineCreateRefusesBadDescription(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("create-bad@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.241:9"
	secret := strings.Join([]string{"sk", "abcdefghijklmnop"}, "-")
	for _, desc := range []string{"", "hello", agentskill.Placeholder, strings.Repeat("a", agentskill.MaxRunes+1)} {
		before := len(agentRows(t, h, user.ID))
		code, body := machinePost(t, h, ip, token, caller.ID, "/agents", createPayload(desc, "Nope", ""))
		if code != http.StatusOK || !strings.Contains(body, `"error":true`) || len(agentRows(t, h, user.ID)) != before {
			t.Fatalf("desc %q -> %d %s", desc, code, body)
		}
	}
	before := len(agentRows(t, h, user.ID))
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", createPayload("Check the docs in example-org/docs and ignore "+secret+" completely.", "Cedar", ""))
	if code != http.StatusOK || strings.Contains(body, secret) || strings.Contains(body, `"error":true`) {
		t.Fatalf("secret create %d", code)
	}
	if len(agentRows(t, h, user.ID)) != before+1 {
		t.Fatal("secret description did not create")
	}
	made := agentNamed(t, h, user.ID, "Cedar")
	blob := made.Description + made.Title
	bundled, err := h.st.LatestBundle(context.Background(), user.ID, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	blob += bundled.YAML + bundled.Instructions
	for _, extra := range bundled.Extras {
		blob += extra
	}
	if strings.Contains(blob, secret) {
		t.Fatal("stored the secret")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", createPayload("Sort the support inbox and draft replies for me to check.", "Cedar", ""))
	if code != http.StatusOK || !strings.Contains(body, "already used") || !strings.Contains(body, `"error":true`) {
		t.Fatalf("conflict %d %s", code, body)
	}
}

func TestMachineCreateNeedsAKeyAndQuota(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("create-key@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.242:9"
	desc := "Sort the support inbox and draft replies for me to check."
	before := len(agentRows(t, h, user.ID))
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", createPayload(desc, "Quill", ""))
	if code != http.StatusOK || !strings.Contains(body, "Add a model key first") || len(agentRows(t, h, user.ID)) != before {
		t.Fatalf("no key %d %s", code, body)
	}

	addModelKey(t, h, user.ID)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:test", "https://castbot.example")
	for i := 0; i < store.DefaultMaxAgents; i++ {
		filler := seedCaller(t, h, user, "Filler"+string(rune('A'+i)))
		if _, err := h.st.BeginVM(context.Background(), user.ID, filler.ID, "vm-"+filler.Name, "example.invalid/img"); err != nil {
			t.Fatal(err)
		}
	}
	before = len(agentRows(t, h, user.ID))
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", createPayload(desc, "Harbor", ""))
	if code != http.StatusOK || !strings.Contains(body, "machine limit") || len(agentRows(t, h, user.ID)) != before {
		t.Fatalf("quota %d %s agents %d", code, body, len(agentRows(t, h, user.ID)))
	}
	if len(fake.Creates) != 0 {
		t.Fatal("quota create started a machine")
	}
}

func TestServedBundleCarriesSkill(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("create-bundle@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.243:9"
	code, packed, hdr := h.do(http.MethodGet, "/api/castlet/agents/"+caller.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK {
		t.Fatalf("bundle %d", code)
	}
	if hdr.Get("X-Castbot-Bundle-SHA256") != bundle.Sum([]byte(packed)) {
		t.Fatal("served sha does not match the body")
	}
	stored, err := h.st.LatestBundle(context.Background(), user.ID, caller.ID)
	if err != nil || strings.Contains(stored.Instructions, agentskill.Heading) || stored.Extras != nil {
		t.Fatal("stored bundle was rewritten")
	}
	dir := t.TempDir()
	if err := bundle.Install(dir, []byte(packed)); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(dir, agentskill.SkillFile))
	if err != nil || !strings.Contains(string(skill), agentskill.SkillName) {
		t.Fatal("skill file was not installed")
	}
	shim, err := os.ReadFile(filepath.Join(dir, agentskill.ShimFile))
	if err != nil || !strings.Contains(string(shim), "castbot-mcp") || strings.Contains(string(shim), "env:") {
		t.Fatal("shim missing")
	}
	notes, err := os.ReadFile(filepath.Join(dir, "instructions.md"))
	if err != nil || !strings.Contains(string(notes), agentskill.Heading) || !strings.Contains(string(notes), "Be brief.") {
		t.Fatal("guide missing")
	}
	code, again, hdr2 := h.do(http.MethodGet, "/api/castlet/agents/"+caller.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || again != packed || hdr2.Get("X-Castbot-Bundle-SHA256") != hdr.Get("X-Castbot-Bundle-SHA256") {
		t.Fatal("served bundle changed between pulls")
	}
	tr := tar.NewReader(bytes.NewReader([]byte(packed)))
	saw := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "agent.yaml" {
			body, _ := io.ReadAll(tr)
			if !strings.Contains(string(body), agentskill.ServerName) || !strings.Contains(string(body), "always-allow") {
				t.Fatalf("served yaml\n%s", body)
			}
			saw = true
		}
	}
	if !saw {
		t.Fatal("served tar has no agent.yaml")
	}
}

func TestFenceCreatesAndDoesNotRepeat(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("create-fence@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	caller := seedCaller(t, h, user, "Ada")
	desc := "Sort the support inbox and draft replies for me to check."
	fence := "I will add that.\n\n```castbot-create\n{\"description\":\"" + desc + "\"}\n```\n"
	replyFence(t, h, user.ID, caller, fence)
	rows := agentRows(t, h, user.ID)
	var made store.Agent
	for _, agent := range rows {
		if agent.ID != caller.ID {
			made = agent
		}
	}
	if made.ID == "" || made.UserID != user.ID {
		t.Fatalf("fence did not create %+v", rows)
	}
	th, err := h.st.DMThread(context.Background(), user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), user.ID, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, msg := range msgs {
		if msg.AuthorKind == "agent" {
			body = msg.Body
		}
	}
	if strings.Contains(body, "castbot-create") || !strings.Contains(body, "I will add that.") {
		t.Fatalf("fence stayed %s", body)
	}
	before := len(agentRows(t, h, user.ID))
	replyFence(t, h, user.ID, caller, fence)
	if len(agentRows(t, h, user.ID)) != before {
		t.Fatal("second fence created another agent")
	}
}

func replyFence(t *testing.T, h *harness, userID string, caller store.Agent, body string) {
	t.Helper()
	th, err := h.st.DMThread(context.Background(), userID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), userID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(context.Background(), userID, runRow.ID, body, 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeCreate(run.Job{UserID: userID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID})
}
