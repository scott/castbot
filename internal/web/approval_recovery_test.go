package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

// respondCounter counts permission/respond posts before passing them on.
type respondCounter struct {
	next  http.Handler
	posts atomic.Int64
}

func (c *respondCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if strings.Contains(string(body), "permission/respond") {
			c.posts.Add(1)
		}
	}
	c.next.ServeHTTP(w, r)
}

// TestRecoveryRepostsJournaledDecision covers a crash between claim and
// confirm. castlet journals a decision before it forwards it, so a journal
// row alone must not stop recovery from posting the decision again.
func TestRecoveryRepostsJournaledDecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		delivered bool
	}{
		{"journaled-not-delivered", false},
		{"delivered-and-journaled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "make deploy"}})
			counter := &respondCounter{next: ask}
			up := httptest.NewServer(counter)
			t.Cleanup(up.Close)
			h := newHarnessWith(t, runtime.NewHTTP(up.URL))
			h.srv.SetApprovalTimeout(3 * time.Second)
			ip := "203.0.113.148:9"
			user, secret := h.account("rr3-recover@example.com", "Demo User", "correct-horse-1")
			h.login("rr3-recover@example.com", "correct-horse-1", secret, ip, false)
			agent := h.newAgent(ip, "Atlas")
			sendPrompt(t, h, agent, ip, "go")
			ap := waitPending(t, h, user.ID)
			ctx := context.Background()
			if tc.delivered {
				body := `{"jsonrpc":"2.0","id":"` + ap.RequestID + `","method":"permission/respond","params":{"id":"` + ap.RequestID + `","approved":true}}`
				res, err := http.Post(up.URL+"/", "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
			}
			before := counter.posts.Load()
			if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			if _, err := h.st.IngestJournal(ctx, user.ID, agent.ID, []store.JournalEntry{
				{Seq: 1, At: "2026-11-11T01:00:00Z", Caller: "control_plane", Kind: "decision", RequestID: ap.RequestID, Decision: "approved"},
			}); err != nil {
				t.Fatal(err)
			}
			h.srv.recoverDecisions(ctx, 0)
			if got := counter.posts.Load() - before; got != 1 {
				t.Fatalf("recovery posted %d times, want 1", got)
			}
			if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
				t.Fatalf("runtime got %#v", calls)
			}
			row, err := h.st.GetApproval(ctx, user.ID, ap.ID)
			if err != nil || row.Status != "approved" || !row.Sent {
				t.Fatalf("row %#v %v", row, err)
			}
			assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
			assertAudit(t, h, user.ID, "approval.inactive", "", "", 0)
			// The run finishes instead of waiting on a prompt nobody answers.
			deadline := time.Now().Add(6 * time.Second)
			for {
				run, err := h.st.GetRun(ctx, user.ID, ap.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if run.Status == "done" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("run stuck in %s", run.Status)
				}
				time.Sleep(50 * time.Millisecond)
			}
			assertAudit(t, h, user.ID, "approval.expired", "", "", 0)
		})
	}
}

// TestRecoveryJournalMismatchKeepsRowStatus checks that the audit follows the
// row, not the journal, when the prompt is gone and the two disagree.
func TestRecoveryJournalMismatchKeepsRowStatus(t *testing.T) {
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
	up := httptest.NewServer(&loseAsk{ask: ask})
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	var logs bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	ip := "203.0.113.149:9"
	user, secret := h.account("rr3-mismatch@example.com", "Demo User", "correct-horse-1")
	h.login("rr3-mismatch@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	ctx := context.Background()
	if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, err := h.st.IngestJournal(ctx, user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-11T01:00:00Z", Caller: "control_plane", Kind: "decision", RequestID: ap.RequestID, Decision: "denied"},
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.recoverDecisions(ctx, 0)
	row, err := h.st.GetApproval(ctx, user.ID, ap.ID)
	if err != nil || row.Status != "approved" || !row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
	assertAudit(t, h, user.ID, "approval.denied", "", "", 0)
	if !strings.Contains(logs.String(), "approval journal mismatch") || !strings.Contains(logs.String(), "journal_decision=denied") {
		t.Fatalf("mismatch not logged: %s", logs.String())
	}
}

// dropFirstRespond fails permission/respond while drop is set, the way a
// runtime that is restarting answers. Everything else reaches the Ask.
type dropFirstRespond struct {
	ask  *runtime.Ask
	drop atomic.Bool
}

func (d *dropFirstRespond) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if d.drop.Load() && strings.Contains(string(body), "permission/respond") {
		http.Error(w, "runtime restarting", http.StatusBadGateway)
		return
	}
	d.ask.ServeHTTP(w, r)
}

// TestRecoveryDeliversCastletJournaledDecision runs through a real castlet.
// castlet journals the decision and then the runtime rejects it, so the
// journal holds a decision the runtime never got. Recovery must post again.
func TestRecoveryDeliversCastletJournaledDecision(t *testing.T) {
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "make deploy"}})
	rt := &dropFirstRespond{ask: ask}
	up := httptest.NewServer(rt)
	t.Cleanup(up.Close)
	const agentToken = "agent-token-demo"
	sup, err := castletNew(t, "", up.URL, "", agentToken)
	if err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewServer(sup)
	t.Cleanup(guest.Close)
	client := runtime.NewHTTP(guest.URL)
	client.SetToken(agentToken)
	h := newHarnessWith(t, client)
	ip := "203.0.113.150:9"
	user, secret := h.account("rr3-castlet@example.com", "Demo User", "correct-horse-1")
	h.login("rr3-castlet@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	ctx := context.Background()
	if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	// The click's post goes through castlet, which journals it, and the
	// runtime drops it. castbot then stops before it confirms the claim.
	rt.drop.Store(true)
	if err := client.Respond(ctx, ap.RequestID, true); err == nil {
		t.Fatal("respond should have failed")
	}
	if calls := ask.Calls(); len(calls) != 0 {
		t.Fatalf("runtime got %#v", calls)
	}
	raw := getJournal(t, guest.URL, agentToken)
	var pulled struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(raw), &pulled); err != nil {
		t.Fatal(err)
	}
	journaled := false
	for _, e := range pulled.Entries {
		if e.Kind == "decision" && e.RequestID == ap.RequestID && e.Decision == "approved" {
			journaled = true
		}
	}
	if !journaled {
		t.Fatalf("castlet did not journal the decision: %s", raw)
	}
	if _, err := h.st.IngestJournal(ctx, user.ID, agent.ID, pulled.Entries); err != nil {
		t.Fatal(err)
	}
	rt.drop.Store(false)
	h.srv.recoverDecisions(ctx, 0)
	if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
		t.Fatalf("runtime got %#v after recovery", calls)
	}
	row, err := h.st.GetApproval(ctx, user.ID, ap.ID)
	if err != nil || row.Status != "approved" || !row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
	assertAudit(t, h, user.ID, "approval.inactive", "", "", 0)
}
