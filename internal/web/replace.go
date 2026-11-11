package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

const replaceSentence = "This machine is being replaced."

type replaceStep struct {
	Title, Detail string
	Done, Now     bool
}

type histItem struct {
	When, Text string
}

type replaceView struct {
	ImageTag, ImageLine                         string
	Update, Same, HasImage                      bool
	HintBold, HintRest                          string
	Reason, ReasonHref, ReasonLabel             string
	Lock, LockLine, Already                     bool
	Steps                                       []replaceStep
	Fine                                        string
	Banner, BannerBold, BannerRest, BannerExtra string
	BannerWarn                                  string
	TryAgain                                    bool
	CanReplace, ShowButton                      bool
	Confirm, PausedNote                         bool
	NowTag, NextTag                             string
	ChatBold, ChatRest                          string
	History                                     []histItem
	NoMachine                                   bool
	LogNote                                     string
	Poll                                        bool
}

func (s *Server) attachReplace(ctx context.Context, user store.User, agent store.Agent, vm machineView, p *page) {
	view := s.replaceView(ctx, user, agent, vm, false)
	p.Replace = view
	if view.Lock {
		markReplacing(p, agent.ID)
	}
}

func markReplacing(p *page, id string) {
	title := p.Agent.Title
	if title == "" {
		title = p.Agent.Name
	}
	sub := title + " · Replacing machine"
	p.Agent.Dot = "start"
	p.Agent.ChipClass = "start"
	p.Agent.ChipLabel = "Replacing"
	p.Agent.ChipMeta = ""
	p.Agent.Sub = sub
	for i := range p.Sections {
		for j := range p.Sections[i].Agents {
			if p.Sections[i].Agents[j].ID == id {
				p.Sections[i].Agents[j].Dot = "start"
				p.Sections[i].Agents[j].Sub = sub
				p.Sections[i].Agents[j].SubClass = ""
			}
		}
	}
}

func (s *Server) replaceView(ctx context.Context, user store.User, agent store.Agent, vm machineView, confirm bool) replaceView {
	loc := userLocation(user.Timezone)
	_, image, _ := s.hostConfig()
	next := replace.ImageTag(image)
	var out replaceView
	out.NextTag = next
	live, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	has := err == nil
	if has && live.Image != "" {
		out.HasImage = true
		out.NowTag = replace.ImageTag(live.Image)
		out.ImageTag = out.NowTag
		if next != "" && out.NowTag != next {
			out.Update = true
			out.ImageLine = out.NowTag + " · Update available"
			out.HintBold = "A newer runtime is available (" + next + ")."
			out.HintRest = "Replace machine moves " + agent.Name + " onto it and keeps the agent, its chats and its memory."
		} else if next != "" {
			out.Same = true
			out.ImageLine = out.NowTag
			out.HintBold = agent.Name + " is on the current runtime (" + next + ")."
			out.HintRest = "You can still replace the machine if it misbehaves; the agent is kept."
		}
	}
	row, rowErr := s.store.LatestReplacement(ctx, user.ID, agent.ID)
	running := rowErr == nil && row.State == "running"
	if !has && image != "" && vm.CanStart && !vm.CanRetry {
		out.NoMachine = true
		out.ShowButton = false
	}
	if has && image != "" && (live.State == "running" || live.State == "paused" || live.State == "creating" || live.State == "failed") {
		out.ShowButton = true
	}
	if running {
		out.HintBold, out.HintRest = "", ""
		out = s.progressView(out, row, user, loc)
		out.Lock = true
		out.CanReplace = false
		out.ShowButton = true
		out.Poll = true
		if strings.HasPrefix(row.Actor, "cli") {
			out.Already = true
			out.Reason = "A replacement is already running."
		} else {
			out.LockLine = true
		}
		n, title, _ := replace.UIStep(row.Step)
		out.ChatBold = "Replacing the machine: " + strings.ToLower(title) + "."
		out.ChatRest = fmt.Sprintf("Step %d of 4. Your chats are safe.", n)
	} else if rowErr == nil && s.store.ReplacementAge(row.CreatedAt) < 24*time.Hour && row.FinishedAt != "" && s.store.ReplacementAge(row.FinishedAt) < 24*time.Hour {
		out = s.resultView(ctx, out, row, user.ID, agent)
	}
	if !running && has && image != "" {
		_, line, err := s.store.ReplacePrecheck(ctx, user.ID, agent.ID)
		if err == nil && line != "" && line != "This agent has no machine yet." {
			out.Reason = line
			out.CanReplace = false
			switch line {
			case "Finish or stop the reply in progress first.":
				out.ReasonHref = "/agents/" + url.PathEscape(agent.ID)
				out.ReasonLabel = "Go to chat"
			case "Answer the waiting approval first.":
				out.ReasonHref = "/approvals"
				out.ReasonLabel = "Review approval"
			}
		} else if err == nil && line == "" {
			out.CanReplace = true
		}
	}
	if confirm && out.CanReplace {
		out.Confirm = true
		out.PausedNote = has && live.State == "paused"
	}
	out.History = s.replaceHistory(ctx, user, agent.ID, loc)
	if out.Banner == "ok" {
		out.LogNote = "Machine logs from the new machine. Logs from the old machine were not kept."
	} else if out.HasImage || out.NoMachine || out.Lock {
		out.LogNote = "Machine logs from castlet are kept on the machine only."
	}
	return out
}

func (s *Server) progressView(out replaceView, row store.Replacement, user store.User, loc *time.Location) replaceView {
	n, _, _ := replace.UIStep(row.Step)
	titles := []string{"Starting a new machine", "Copying memory and files", "Switching over", "Removing the old machine"}
	for i, title := range titles {
		step := replaceStep{Title: title, Done: i+1 < n, Now: i+1 == n}
		if step.Now && i == 0 {
			step.Detail = "Waiting for the new machine to be ready · up to 4 min"
		}
		if step.Now && i == 1 && (row.MemoryBytes > 0 || row.FilesTotal > 0) {
			var parts []string
			if row.Woke {
				parts = append(parts, "Woke the old machine")
			}
			parts = append(parts, "memory "+formatBytes(row.MemoryBytes))
			parts = append(parts, fmt.Sprintf("%d of %d files", row.FilesDone, row.FilesTotal))
			step.Detail = strings.Join(parts, " · ")
		}
		out.Steps = append(out.Steps, step)
	}
	when := clock(row.CreatedAt, loc)
	if strings.HasPrefix(row.Actor, "web:") {
		out.Fine = "This panel updates on its own every 2 s and keeps going if you close the page. Started " + when + " by " + strings.TrimPrefix(row.Actor, "web:") + "."
	} else {
		out.Fine = "Started " + when + " from the admin CLI on the host. This panel updates on its own every 2 s."
	}
	return out
}

func (s *Server) resultView(ctx context.Context, out replaceView, row store.Replacement, userID string, agent store.Agent) replaceView {
	if row.State == "succeeded" {
		out.Banner = "ok"
		out.BannerBold = "Machine replaced."
		rest := "Now on " + replace.ImageTag(row.NewImage) + "."
		if !row.NoCopy {
			rest = "Now on " + replace.ImageTag(row.NewImage) + ". Copied memory and " + fmt.Sprintf("%d", row.FilesTotal) + " files."
		}
		out.BannerRest = rest
		if s.hasActiveCLI(ctx, userID, agent.ID) {
			out.BannerExtra = "Run Add to Cast CLI again to reconnect the Cast CLI."
		}
		if row.DeletePending {
			out.BannerWarn = "The old machine is not deleted yet. castbot will keep trying."
		}
		out.NowTag = replace.ImageTag(row.NewImage)
		out.ImageTag = out.NowTag
		out.ImageLine = out.NowTag
		out.Update = false
		out.Same = true
		if out.NextTag != "" {
			out.HintBold = agent.Name + " is on the current runtime (" + out.NextTag + ")."
			out.HintRest = "You can still replace the machine if it misbehaves; the agent is kept."
		}
		return out
	}
	if row.State == "failed" {
		out.Banner = "err"
		out.BannerBold = "The machine was not replaced."
		out.BannerRest = "Your agent is still on the old machine (" + replace.ImageTag(row.OldImage) + ") and nothing was lost."
		out.BannerExtra = replace.Reason(row.Class)
		out.BannerWarn = "Next: try again in a few minutes. " + agent.Name + " keeps working on the old machine in the meantime. If it keeps failing, ask your castbot admin."
		out.TryAgain = true
	}
	return out
}

func (s *Server) hasActiveCLI(ctx context.Context, userID, agentID string) bool {
	tokens, err := s.store.ListCLITokens(ctx, userID, agentID)
	if err != nil {
		return false
	}
	for _, tok := range tokens {
		if tok.RevokedAt == "" {
			return true
		}
	}
	return false
}

func (s *Server) replaceHistory(ctx context.Context, user store.User, agentID string, loc *time.Location) []histItem {
	events, err := s.store.ListVMEvents(ctx, user.ID, agentID)
	if err != nil || len(events) == 0 {
		return nil
	}
	actor, class := "", ""
	if row, err := s.store.LatestReplacement(ctx, user.ID, agentID); err == nil {
		actor, class = row.Actor, row.Class
	}
	var out []histItem
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == store.EventReplaceNoCopy {
			continue
		}
		text := historyLabel(ev.Kind)
		if ev.Kind == store.EventReplaceRequest {
			if strings.HasPrefix(actor, "web:") {
				text = "Replace requested by " + strings.TrimPrefix(actor, "web:")
			} else if strings.HasPrefix(actor, "cli") || actor == "cli" {
				text = "Replace requested from the admin CLI"
			}
		}
		if ev.Kind == store.EventReplaceFailed && class != "" {
			text = "Replace failed at " + class + ", new machine deleted"
		}
		if text == "" {
			continue
		}
		out = append(out, histItem{When: historyWhen(ev.At, loc), Text: text})
	}
	return out
}

func historyLabel(kind string) string {
	switch kind {
	case store.EventReplaceRequest:
		return "Replace requested"
	case store.EventReplaceSwitch:
		return "Switched to the new machine"
	case store.EventReplaceFailed:
		return "Replace failed, new machine deleted"
	case store.EventReplaceDelete:
		return "Old machine delete still pending"
	case store.EventWakeRequested, store.EventWakeRunning:
		return "Woke for a message"
	case store.EventPauseRequested, store.EventPauseObserved:
		return "Paused after idle"
	case store.EventRunning:
		return "Machine running"
	case store.EventCreateRequested, store.EventCreateAccepted:
		return "Machine create requested"
	case store.EventDelete:
		return "Machine deleted"
	case store.EventGuestReady:
		return "Guest ready"
	default:
		return ""
	}
}

func historyWhen(stamp string, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return stamp
	}
	now := time.Now()
	if loc != nil {
		now = now.In(loc)
	}
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "Today " + t.Format("3:04 PM")
	}
	return t.Format("Jan 2, 3:04 PM")
}

func clock(stamp string, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return stamp
	}
	return t.Format("3:04 PM")
}

func formatBytes(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return trimFloat(float64(n)/1024) + " KB"
	}
	return trimFloat(float64(n)/(1024*1024)) + " MB"
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

func (s *Server) confirmReplace(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), confirmKey{}, true))
	s.renderChat(w, r, user, r.PathValue("id"), false, "machine", http.StatusOK, "")
}

func (s *Server) machineCard(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, id); s.missing(w, err) {
		return
	}
	// A finished replace has to reload the whole page. The poll only swaps
	// the machine panel, so the chip and the sidebar would stay on Replacing.
	if !s.replacing(r.Context(), user.ID, id) {
		w.Header().Set("HX-Redirect", "/agents/"+url.PathEscape(id)+"/machine")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.renderChat(w, r, user, id, false, "machine", http.StatusOK, "")
}

func (s *Server) postReplace(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.replacing(r.Context(), user.ID, agent.ID) {
		writeText(w, http.StatusConflict, "A replacement is already running.\n")
		return
	}
	_, line, err := s.store.ReplacePrecheck(r.Context(), user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if line != "" {
		writeText(w, http.StatusConflict, line+"\n")
		return
	}
	h, image, publicURL := s.hostConfig()
	if h == nil || image == "" || publicURL == "" {
		writeText(w, http.StatusBadRequest, "No machine host is configured.\n")
		return
	}
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		_, err := replace.Run(ctx, s.store, h, replace.Options{
			UserID: user.ID, AgentID: agent.ID,
			ActorKind: "user", ActorID: user.ID, Actor: "web:" + user.Email,
			Image: image, PublicURL: publicURL, DataDir: s.dataDir,
		})
		if err != nil {
			log.Printf("replace finished agent %s class %s", agent.ID, replaceClass(err))
		}
	}()
	target := "/agents/" + url.PathEscape(agent.ID) + "/machine"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func replaceClass(err error) string {
	var kept *replace.Kept
	if errors.As(err, &kept) {
		return kept.Error()
	}
	var human *replace.NeedsHuman
	if errors.As(err, &human) {
		return "delete pending"
	}
	return "error"
}

func (s *Server) replacing(ctx context.Context, userID, agentID string) bool {
	_, err := s.store.ActiveReplacement(ctx, userID, agentID)
	return err == nil
}

func (s *Server) refuseReplacing(w http.ResponseWriter, r *http.Request, userID, agentID string) bool {
	if !s.replacing(r.Context(), userID, agentID) {
		return false
	}
	writeText(w, http.StatusConflict, replaceSentence+"\n")
	return true
}

// SetDataDir is the directory for replace temp files. It is the database directory.
func (s *Server) SetDataDir(dir string) {
	s.mu.Lock()
	s.dataDir = strings.TrimSpace(dir)
	s.mu.Unlock()
}

// RecoverReplacements finishes or rolls back replaces that were in progress.
func (s *Server) RecoverReplacements(ctx context.Context) {
	h, _, _ := s.hostConfig()
	if h == nil {
		return
	}
	if err := replace.Recover(ctx, s.store, h); err != nil {
		log.Printf("replace recovery failed")
	}
}

// RetryReplaceDeletes keeps trying to delete an old machine after the switch.
func (s *Server) RetryReplaceDeletes(ctx context.Context) {
	delay := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		h, _, _ := s.hostConfig()
		if h != nil {
			_ = replace.RetryDeletes(context.Background(), s.store, h)
		}
		if delay < 60*time.Second {
			delay *= 2
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
		}
	}
}

type confirmKey struct{}

func wantsConfirm(r *http.Request) bool {
	v, _ := r.Context().Value(confirmKey{}).(bool)
	return v
}
