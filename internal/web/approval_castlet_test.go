package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castlet"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const castletTestToken = "agent-token-demo"

// castletHost sends Approve and PullJournal through a real castlet, the way
// host.MicroVM reaches the guest. Everything else is the fake host.
type castletHost struct {
	*host.Fake
	url string
}

func (c *castletHost) call(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Cast-Authorization", "Bearer "+castletTestToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, raw, nil
}

func (c *castletHost) Approve(ctx context.Context, _, _, requestID string, allow bool) error {
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "permission/respond", "params": map[string]any{"id": requestID, "approved": allow}})
	if err != nil {
		return err
	}
	code, raw, err := c.call(ctx, http.MethodPost, "/", payload)
	if err != nil {
		return err
	}
	if code != http.StatusOK && code != http.StatusAccepted && code != http.StatusNoContent {
		return fmt.Errorf("approve status %d", code)
	}
	if err := runtime.PermissionError(raw); err != nil {
		if err == runtime.ErrNoRequest {
			return host.ErrNoRequest
		}
		return err
	}
	return nil
}

func (c *castletHost) PullJournal(ctx context.Context, _, _ string, after int) ([]byte, error) {
	code, raw, err := c.call(ctx, http.MethodGet, "/castlet/journal?after="+strconv.Itoa(after), nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("journal status %d", code)
	}
	return raw, nil
}

// journalDecisions lists the decisions castlet journaled for one request.
func (c *castletHost) journalDecisions(t *testing.T, requestID string) []string {
	t.Helper()
	code, raw, err := c.call(context.Background(), http.MethodGet, "/castlet/journal?after=0", nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("journal %d %v", code, err)
	}
	var body struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range body.Entries {
		if e.Kind == "decision" && e.RequestID == requestID {
			out = append(out, e.Decision)
		}
	}
	return out
}

// livePrompt opens a turn through castlet and returns the permission id.
func castletLivePrompt(t *testing.T, url string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"r1:0","method":"message/send","params":{"message":{"role":"user","messageId":"m1","parts":[{"kind":"text","text":"go"}]},"metadata":{"sessionId":"S","streamId":"st1","streamOffset":0}}}`
	req, err := http.NewRequest(http.MethodPost, url+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Cast-Authorization", "Bearer "+castletTestToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		var f struct {
			Result struct {
				Metadata struct {
					PermissionRequest struct {
						ID string `json:"id"`
					} `json:"permissionRequest"`
				} `json:"metadata"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &f) == nil && f.Result.Metadata.PermissionRequest.ID != "" {
			go func() { _, _ = io.Copy(io.Discard, res.Body) }()
			return f.Result.Metadata.PermissionRequest.ID
		}
	}
	t.Fatal("no permission id")
	return ""
}

// TestRecoveryThroughRealCastlet runs claim recovery against a real castlet
// Supervisor. castlet journals every permission/respond before it forwards
// it, including recovery's own re-post, so recovery must decide from the
// journal as it was before that re-post.
func TestRecoveryThroughRealCastlet(t *testing.T) {
	for i, tc := range []struct {
		name  string
		setup string
	}{
		{"lost-prompt-run-streaming", "lost"},
		{"lost-prompt-run-failed", "lost-failed"},
		{"delivered-before-crash", "delivered"},
		{"journaled-not-delivered", "undelivered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ip := fmt.Sprintf("203.0.113.%d:9", 160+i)
			h, fake, agent := machineUser(t, ip)
			rec := &slogRec{}
			h.srv.SetLogger(slog.New(rec))
			ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "make deploy"}})
			rt := &dropFirstRespond{ask: ask}
			up := httptest.NewServer(rt)
			t.Cleanup(up.Close)
			sup, err := castlet.New(castlet.Config{Token: castletTestToken, RuntimeURL: up.URL, AgentDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			sup.MarkReady()
			cs := httptest.NewServer(sup)
			t.Cleanup(cs.Close)
			ctx := context.Background()
			vm := fmt.Sprintf("vm-recover-%d", i)
			if _, err := h.st.BeginVM(ctx, agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
				t.Fatal(err)
			}
			if err := h.st.FinishVM(ctx, agent.UserID, agent.ID, vm, "https://vm.example", "running", ""); err != nil {
				t.Fatal(err)
			}
			plain, err := h.st.IssueBootstrap(ctx, agent.UserID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			token, err := h.st.ExchangeBootstrap(ctx, agent.ID, plain)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.st.SaveVMToken(ctx, agent.ID, token); err != nil {
				t.Fatal(err)
			}
			fake.Machines[vm] = host.Machine{ID: vm, State: "running", Endpoint: "https://vm.example"}
			ch := &castletHost{Fake: fake, url: cs.URL}
			h.srv.SetHost(ch, "example.invalid/castbot-runtime:s07", "https://castbot.example")

			reqID := "recover-gone-" + strconv.Itoa(i)
			if tc.setup == "delivered" || tc.setup == "undelivered" {
				reqID = castletLivePrompt(t, cs.URL)
			}
			threads, err := h.st.ListThreadsByAgent(ctx, agent.UserID, agent.ID)
			if err != nil || len(threads) == 0 {
				t.Fatalf("threads %v", err)
			}
			run, err := h.st.CreateRun(ctx, agent.UserID, threads[0].ID, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.st.MarkStreaming(ctx, agent.UserID, run.ID); err != nil {
				t.Fatal(err)
			}
			ap, err := h.st.RecordPermission(ctx, agent.UserID, run.ID, json.RawMessage(`{"id":"`+reqID+`","tool":"bash","arguments":{"command":"make deploy"}}`), time.Now().Add(time.Hour), 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := h.st.ClaimDecision(ctx, agent.UserID, ap.ID, run.ID, "approved", agent.UserID); err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			switch tc.setup {
			case "delivered":
				// The click's post reached the runtime; castbot stopped before it confirmed.
				if err := ch.Approve(ctx, "", "", reqID, true); err != nil {
					t.Fatal(err)
				}
			case "undelivered":
				// castlet journals the click's post, and the runtime rejects it.
				rt.drop.Store(true)
				if err := ch.Approve(ctx, "", "", reqID, true); err == nil {
					t.Fatal("post should have failed")
				}
				rt.drop.Store(false)
				if got := ch.journalDecisions(t, reqID); len(got) != 1 || got[0] != "approved" {
					t.Fatalf("castlet journal %v", got)
				}
			case "lost-failed":
				if _, err := h.st.FailRun(ctx, agent.UserID, run.ID, "The reply was interrupted.", 1); err != nil {
					t.Fatal(err)
				}
			}
			before := len(ask.Calls())
			h.srv.recoverDecisions(ctx, 0)
			row, err := h.st.GetApproval(ctx, agent.UserID, ap.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := ask.Calls()
			journal := ch.journalDecisions(t, reqID)
			switch tc.setup {
			case "lost":
				if row.Status != "inactive" || len(calls) != 0 {
					t.Fatalf("row %s calls %v journal %v", row.Status, calls, journal)
				}
				// The re-post itself was journaled, and recovery did not trust it.
				if len(journal) != 1 || journal[0] != "approved" {
					t.Fatalf("journal %v", journal)
				}
				assertAudit(t, h, agent.UserID, "approval.approved", "", "", 0)
				assertAudit(t, h, agent.UserID, "approval.inactive", "user", agent.UserID, 1)
			case "lost-failed":
				if row.Status != "inactive" || len(calls) != 0 {
					t.Fatalf("row %s calls %v journal %v", row.Status, calls, journal)
				}
				// Recovery posted Deny, never Approve.
				if len(journal) != 1 || journal[0] != "denied" {
					t.Fatalf("journal %v", journal)
				}
				assertAudit(t, h, agent.UserID, "approval.approved", "", "", 0)
				assertAudit(t, h, agent.UserID, "approval.inactive", "user", agent.UserID, 1)
			case "delivered":
				if row.Status != "approved" || !row.Sent || len(calls) != 1 || !calls[0] || len(calls) != before {
					t.Fatalf("row %s sent %v calls %v", row.Status, row.Sent, calls)
				}
				assertAudit(t, h, agent.UserID, "approval.approved", "user", agent.UserID, 1)
				assertAudit(t, h, agent.UserID, "approval.inactive", "", "", 0)
			case "undelivered":
				if before != 0 || len(calls) != 1 || !calls[0] {
					t.Fatalf("runtime got %v before %d", calls, before)
				}
				if row.Status != "approved" || !row.Sent {
					t.Fatalf("row %s sent %v", row.Status, row.Sent)
				}
				assertAudit(t, h, agent.UserID, "approval.approved", "user", agent.UserID, 1)
				assertAudit(t, h, agent.UserID, "approval.inactive", "", "", 0)
			}
			if n := len(journalMismatchLines(rec)); n != 0 {
				t.Fatalf("mismatch warnings %d", n)
			}
		})
	}
}

func journalMismatchLines(rec *slogRec) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, l := range rec.lines {
		if strings.Contains(l, "approval journal mismatch") {
			out = append(out, l)
		}
	}
	return out
}
