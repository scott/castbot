package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

// stillRunning is the page line when Cancel cannot end the prompt.
const stillRunning = "The prompt is still running."

func openRunStatus(status string) bool {
	switch status {
	case "queued", "streaming", "waiting_approval":
		return true
	default:
		return false
	}
}

// stopTarget is the run id for the Stop control.
// A Cast CLI thread has no Stop. A finished run has none either.
func (s *Server) stopTarget(ctx context.Context, userID string, row store.Run) string {
	if !openRunStatus(row.Status) {
		return ""
	}
	th, err := s.store.GetThread(ctx, userID, row.ThreadID)
	if err != nil || th.Kind == "cli" {
		return ""
	}
	return row.ID
}

func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	row, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	if !openRunStatus(row.Status) || s.stopTarget(r.Context(), user.ID, row) == "" {
		s.answerStop(w, r, user, row, "", false, false)
		return
	}
	if row.Status == "queued" {
		stopped, text, changed, err := s.store.StopRun(r.Context(), user.ID, row.ID, false)
		if mapStoreErr(w, r, err) {
			return
		}
		if changed {
			s.finishStop(r.Context(), user, row)
			s.answerStop(w, r, user, stopped, text, true, false)
			return
		}
		fresh, err := s.store.GetRun(r.Context(), user.ID, row.ID)
		if mapStoreErr(w, r, err) {
			return
		}
		if fresh.Status != "streaming" && fresh.Status != "waiting_approval" {
			s.answerStop(w, r, user, fresh, "", false, false)
			return
		}
		row = fresh
	}
	if err := s.cancelGuest(r.Context(), user.ID, row); err != nil {
		s.answerStop(w, r, user, row, "", false, true)
		return
	}
	stopped, text, changed, err := s.store.StopRun(r.Context(), user.ID, row.ID, true)
	if mapStoreErr(w, r, err) {
		return
	}
	if changed {
		// Status stopped reaches the after-run hook, which ends an
		// always-approve grant once no covered run is still open.
		s.finishStop(r.Context(), user, row)
		s.answerStop(w, r, user, stopped, text, true, false)
		return
	}
	fresh, err := s.store.GetRun(r.Context(), user.ID, row.ID)
	if mapStoreErr(w, r, err) {
		return
	}
	s.answerStop(w, r, user, fresh, "", false, false)
}

func (s *Server) finishStop(ctx context.Context, user store.User, row store.Run) {
	s.runs.Hub.Notify(row.ID)
	s.runs.Release(s.stopJob(ctx, user, row), "stopped")
}

func (s *Server) stopJob(ctx context.Context, user store.User, row store.Run) run.Job {
	job := run.Job{UserID: user.ID, ThreadID: row.ThreadID, AgentID: row.AgentID, RunID: row.ID}
	if msg, err := s.store.UserMessageForRun(ctx, user.ID, row.ID); err == nil {
		job.MessageID = msg.ID
		job.Text = msg.Body
	}
	if th, err := s.store.GetThread(ctx, user.ID, row.ThreadID); err == nil {
		job.SessionID = th.CastSessionID
	}
	return job
}

func (s *Server) cancelGuest(ctx context.Context, userID string, row store.Run) error {
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	if h == nil {
		return host.ErrCancel
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, row.AgentID)
	if err != nil || endpoint == "" || token == "" {
		return host.ErrCancel
	}
	return h.Cancel(ctx, endpoint, token, row.ID)
}

func (s *Server) answerStop(w http.ResponseWriter, r *http.Request, user store.User, row store.Run, text string, changed, still bool) {
	hx := r.Header.Get("HX-Request") == "true"
	if still {
		if hx {
			body, err := s.pendingStopHTML(r.Context(), user, row, from(r).CSRF, stillRunning)
			if err != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			writeHTML(w, http.StatusOK, body)
			return
		}
		http.Redirect(w, r, agentThreadPath(row.AgentID, row.ThreadID)+"&notice=running", http.StatusSeeOther)
		return
	}
	if changed || row.Status == "stopped" {
		if hx {
			if text == "" {
				if got, err := s.store.RunText(r.Context(), user.ID, row.ID); err == nil {
					text = got
				}
			}
			writeHTML(w, http.StatusOK, s.stoppedTurnHTML(r.Context(), user, row, text, from(r).CSRF))
			return
		}
		http.Redirect(w, r, agentThreadPath(row.AgentID, row.ThreadID), http.StatusSeeOther)
		return
	}
	if !openRunStatus(row.Status) {
		if hx {
			w.Header().Set("HX-Redirect", agentThreadPath(row.AgentID, row.ThreadID))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, agentThreadPath(row.AgentID, row.ThreadID), http.StatusSeeOther)
		return
	}
	if hx {
		body, err := s.pendingStopHTML(r.Context(), user, row, from(r).CSRF, "")
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		writeHTML(w, http.StatusOK, body)
		return
	}
	http.Redirect(w, r, agentThreadPath(row.AgentID, row.ThreadID), http.StatusSeeOther)
}

func agentThreadPath(agentID, threadID string) string {
	return "/agents/" + url.PathEscape(agentID) + "?thread=" + url.QueryEscape(threadID)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *Server) pendingStopHTML(ctx context.Context, user store.User, row store.Run, csrf, note string) (string, error) {
	agent := agentView{ID: row.AgentID, Color: "c0", Letter: "?"}
	if got, err := s.store.GetAgent(ctx, user.ID, row.AgentID); err == nil {
		agent = presentAgent(got, true, userLocation(user.Timezone))
	}
	view := s.paintLive(ctx, user, row, agent, csrf)
	view.Note = note
	cards, err := s.cardsFor(ctx, user, row.ID, csrf)
	if err != nil {
		return "", err
	}
	view.Cards = append(cards, s.connectCardViews(ctx, user, row.AgentID, csrf)...)
	view.SecretCards = s.secretCardViews(ctx, user, row.AgentID, csrf)
	return execTemplate("pending", view)
}

func (s *Server) stoppedTurnHTML(ctx context.Context, user store.User, row store.Run, text, csrf string) string {
	agent := agentView{ID: row.AgentID, Color: "c0", Letter: "?"}
	if got, err := s.store.GetAgent(ctx, user.ID, row.AgentID); err == nil {
		agent = presentAgent(got, false, userLocation(user.Timezone))
	}
	stamp := s.store.Now().UTC().Format(time.RFC3339)
	var msgs []store.Message
	if strings.TrimSpace(text) != "" {
		msgs = append(msgs, store.Message{
			ID: "stop-reply", AuthorKind: "agent", AuthorID: row.AgentID, Body: text, CreatedAt: stamp,
		})
	}
	msgs = append(msgs, store.Message{
		ID: "stop-note", AuthorKind: "system", AuthorID: row.AgentID, Body: store.StoppedLine, Origin: "notice", CreatedAt: stamp,
	})
	views := presentMessages(msgs, agent, s.store.Now(), userLocation(user.Timezone))
	for i := range views {
		views[i].ShowDay = false
	}
	if rows, err := s.store.ListRunApprovals(ctx, user.ID, row.ID); err == nil {
		var cards []cardView
		for _, item := range rows {
			cards = append(cards, s.presentApproval(item, user, csrf))
		}
		views = weaveCards(views, cards, nil)
	}
	if rows, err := s.store.ListConnectApprovals(ctx, user.ID, row.AgentID, s.store.Now().Add(-2*time.Hour)); err == nil {
		var cards []cardView
		for _, item := range rows {
			if item.RunID != row.ID || item.Status == "consumed" {
				continue
			}
			cards = append(cards, s.presentConnect(user, item, csrf))
		}
		views = weaveCards(views, cards, nil)
	}
	body, err := execTemplate("messages", page{Messages: views})
	if err != nil {
		return ""
	}
	if chrome, cerr := s.badgeChrome(ctx, user, row.AgentID, row.ID); cerr == nil {
		body += chrome
	}
	body += s.sideOOB(ctx, user, row.AgentID)
	return body
}
