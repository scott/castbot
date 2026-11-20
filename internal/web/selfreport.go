package web

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

const reportCap = 7500

type report struct {
	b       strings.Builder
	stopped bool
}

func (r *report) line(s string) {
	s = strings.TrimSpace(s)
	if s == "" || r.stopped {
		return
	}
	if r.b.Len() > 0 && r.b.Len()+1+len(s) > reportCap {
		r.b.WriteString("\nMore is on the screen.")
		r.stopped = true
		return
	}
	if r.b.Len() == 0 && len(s) > reportCap {
		s = cutBytes(s, reportCap-len("\nMore is on the screen."))
		r.b.WriteString(s)
		r.b.WriteString("\nMore is on the screen.")
		r.stopped = true
		return
	}
	if r.b.Len() > 0 {
		r.b.WriteByte('\n')
	}
	r.b.WriteString(s)
}

func (r *report) String() string {
	return r.b.String()
}

func (s *Server) describeMe(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	loc := userLocation(user.Timezone)
	view := presentAgent(agent, false, loc)
	label := view.ChipLabel
	if s.replacing(ctx, user.ID, agent.ID) {
		label = "Replacing"
	}
	var rep report
	rep.line("Name: " + agent.Name)
	if agent.Title != "" {
		rep.line("Title: " + agent.Title)
	}
	job, _ := redactSecrets(clipRunes(strings.TrimSpace(agent.Description), 400))
	if job == "" {
		job = "none"
	}
	rep.line("Job: " + job)
	notes := "none"
	if bundle, err := s.store.LatestBundle(ctx, user.ID, agent.ID); err == nil {
		notes = strings.TrimSpace(bundle.Instructions)
		clipped := clipRunes(notes, 400)
		notes, _ = redactSecrets(clipped)
		if notes == "" {
			notes = "none"
		}
		if clipped != strings.TrimSpace(bundle.Instructions) && notes != "none" {
			notes += " The rest is in the loaded agent file."
		}
	}
	rep.line("Instructions: " + notes)
	model := modelLabel(agent.Model)
	if model == "" {
		model = "none"
	}
	rep.line("Model: " + model)
	rep.line("Color: " + colorName(agent.Color))
	if sec, err := s.store.GetSection(ctx, user.ID, agent.SectionID); err == nil && sec.Name != "" {
		rep.line("Section: " + sec.Name)
	}
	if created := monthDay(agent.CreatedAt, loc); created != "" {
		rep.line("Created: " + created)
	}
	hand := "No"
	if agent.CanDelegate {
		hand = "Yes"
	}
	rep.line("Can hand work to other agents: " + hand)
	activity := strings.TrimSpace(agent.Activity)
	if activity == "" {
		activity = "none"
	}
	rep.line("Activity: " + activity)
	rep.line("Status: " + label)
	rep.line("Size: " + view.SizeLabel)
	rep.line("Auto-pause: " + view.IdleLabel)
	if rep.String() == "" {
		return "That could not be read.", true
	}
	return rep.String(), false
}

func colorName(key string) string {
	if key == "" || key == "c0" {
		return "Default"
	}
	for _, c := range agentPalette() {
		if c.Key == key {
			return c.Name
		}
	}
	return "Default"
}

func (s *Server) accountUsageText(ctx context.Context, user store.User, caller store.Agent) (string, bool) {
	var rep report
	rep.line("This month for " + caller.Name + ": " + s.monthEstimate(ctx, user, caller.ID))
	spans, err := s.store.UsageIn(ctx, user.ID, "", monthStart(s.store.Now(), userLocation(user.Timezone)), s.store.Now())
	if err != nil {
		return "Usage could not be read.", true
	}
	by := map[string]int64{}
	var total int64
	for _, span := range spans {
		n := spanMicro(span)
		by[span.AgentID] += n
		total += n
	}
	rep.line("Account total: " + formatUSD(total))
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return "Usage could not be read.", true
	}
	for _, agent := range agents {
		n := by[agent.ID]
		_, liveErr := s.store.LiveVM(ctx, user.ID, agent.ID)
		if n == 0 && liveErr != nil {
			continue
		}
		rep.line(agent.Name + ": " + formatUSD(n))
	}
	if used, max, err := s.store.Quota(ctx, user.ID); err == nil {
		rep.line(fmt.Sprintf("Machines: %d of %d", used, max))
	}
	if n, err := s.store.UploadUsage(ctx, user.ID, user.WorkspaceID); err == nil {
		rep.line(formatBytes(n) + " of 2 GB")
		if n >= uploadQuota {
			rep.line("Uploads are at the 2 GB limit.")
		} else if n*10 >= uploadQuota*8 {
			rep.line("Uploads are past 80% of 2 GB.")
		}
	}
	rep.line("An estimate, not a bill.")
	rep.line("Billed by your provider.")
	rep.line("castbot never adds a charge on top.")
	return rep.String(), false
}

func (s *Server) machineStatusText(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	loc := userLocation(user.Timezone)
	mv := s.machineFromStore(ctx, user.ID, &agent, false)
	rv := s.replaceView(ctx, user, agent, mv, false)
	var rep report
	rep.line(mv.Lead)
	rep.line("Size: " + sizeLabel(agent.Size))
	rep.line("Auto-pause: " + idleLabel(agent.IdleTimeout))
	if rv.HasImage {
		line := rv.ImageLine
		if line == "" {
			line = rv.NowTag
		}
		if line != "" {
			rep.line("Image: " + line)
		}
	} else {
		rep.line("No image is recorded.")
	}
	if mv.ShowFirst && mv.FirstRunning != "" {
		rep.line("First running: " + clockStamp(mv.FirstRunning, loc))
	}
	if mv.LastWake != "" {
		rep.line("Last wake: " + clockStamp(mv.LastWake, loc))
	}
	if rv.CanReplace {
		rep.line("Replace is available.")
	} else if rv.Reason != "" {
		rep.line(rv.Reason)
	} else if rv.HasImage && !rv.Lock {
		rep.line("Replace is not available.")
	}
	if rv.CanRollback {
		rep.line(fmt.Sprintf("Rollback to %s is available for %d days.", rv.RollbackTag, rv.RollbackDays))
	} else if rv.HasImage && !rv.Lock {
		rep.line("Rollback is not available.")
	}
	if rv.Lock {
		rep.line("This machine is being replaced.")
	}
	if rv.Already {
		rep.line("A replacement is already running.")
	}
	hist := s.replaceHistory(ctx, user, agent.ID, loc)
	if len(hist) == 0 {
		rep.line("No machine history yet.")
	} else {
		rep.line("History")
		for _, item := range hist {
			if item.When != "" {
				rep.line(item.When + ". " + item.Text)
			} else {
				rep.line(item.Text)
			}
		}
	}
	return rep.String(), false
}

func (s *Server) approvalHistoryText(ctx context.Context, user store.User, agent store.Agent, query string) (string, bool) {
	rows, err := s.store.ListAgentApprovals(ctx, user.ID, agent.ID)
	if err != nil {
		return "Approvals could not be read.", true
	}
	loc := userLocation(user.Timezone)
	now := s.store.Now()
	q := strings.ToLower(strings.TrimSpace(query))
	var waiting []cardView
	var earlier []earlierCard
	for i, row := range rows {
		card := s.presentApproval(row, user, "")
		if row.Status == "expired" {
			card.Line = "No answer in 10 min, denied"
		}
		if row.Status == "pending" && row.Origin == "web" {
			if q == "" || strings.Contains(approvalFind(card), q) {
				waiting = append(waiting, card)
			}
			continue
		}
		if card.Fold == "" {
			card.Fold = "Waiting"
		}
		card.Find = approvalFind(card)
		if q != "" && !strings.Contains(card.Find, q) {
			continue
		}
		earlier = append(earlier, earlierCard{view: card, stamp: historyStamp(row), ord: i})
	}
	if connects, err := s.store.ListConnectApprovals(ctx, user.ID, agent.ID, time.Unix(0, 0)); err == nil {
		for _, row := range connects {
			if row.Status != "pending" {
				continue
			}
			card := s.presentConnect(user, row, "")
			if q != "" && !strings.Contains(approvalFind(card), q) {
				continue
			}
			waiting = append(waiting, card)
		}
	}
	sort.SliceStable(earlier, func(i, j int) bool {
		if earlier[i].stamp != earlier[j].stamp {
			return earlier[i].stamp > earlier[j].stamp
		}
		return earlier[i].ord > earlier[j].ord
	})
	if len(waiting) == 0 && len(earlier) == 0 {
		if q != "" {
			return "No approvals match.", false
		}
		return "Nothing is waiting.", false
	}
	var rep report
	if len(waiting) == 0 && q == "" {
		rep.line("Nothing is waiting.")
	}
	if len(waiting) > 0 {
		rep.line("Waiting")
		for _, card := range waiting {
			rep.line(approvalSentence(card))
		}
	}
	if len(earlier) > 0 {
		rep.line("Earlier")
		var last string
		for _, item := range earlier {
			_, label := approvalDayLabel(item.stamp, now, loc)
			if label != last {
				rep.line(label)
				last = label
			}
			rep.line(approvalSentence(item.view))
		}
	}
	return rep.String(), false
}

func approvalSentence(card cardView) string {
	var parts []string
	if card.Fold != "" {
		parts = append(parts, card.Fold)
	}
	if card.Tool != "" {
		parts = append(parts, card.Tool)
	}
	if card.Line != "" {
		parts = append(parts, card.Line)
	}
	if card.When != "" && !strings.Contains(card.Line, card.When) {
		parts = append(parts, card.When)
	}
	if card.From != "" {
		parts = append(parts, card.From)
	}
	if card.Extra != "" {
		parts = append(parts, card.Extra)
	}
	return strings.Join(parts, ". ")
}

func (s *Server) routineRunsText(ctx context.Context, user store.User, caller store.Agent, id string) (string, bool) {
	id = strings.TrimSpace(id)
	loc := userLocation(user.Timezone)
	var routines []store.Routine
	if id != "" {
		row, ok := s.ownRoutine(ctx, user.ID, caller.ID, id)
		if !ok {
			return "That routine was not found.", true
		}
		routines = []store.Routine{row}
	} else {
		rows, err := s.store.ListRoutines(ctx, user.ID, caller.ID)
		if err != nil {
			return "Routine runs could not be read.", true
		}
		if len(rows) == 0 {
			return "No routines yet.", false
		}
		routines = rows
	}
	var rep report
	shown := 0
	for _, routine := range routines {
		runs, err := s.store.ListRoutineRuns(ctx, user.ID, routine.ID, 30)
		if err != nil {
			return "Routine runs could not be read.", true
		}
		for _, run := range runs {
			if shown >= 30 {
				break
			}
			when := run.ScheduledFor
			if when == "" {
				when = run.StartedAt
			}
			line := routine.Name + ". " + routinePill(run.Status)
			if stamp := clockStamp(when, loc); stamp != "" {
				line += ". " + stamp
			}
			if run.ThreadID != "" {
				line += ". Thread " + run.ThreadID
			}
			rep.line(line)
			shown++
		}
	}
	if shown == 0 {
		return "No runs yet.", false
	}
	return rep.String(), false
}

func routinePill(status string) string {
	switch status {
	case store.RoutineRunning:
		return "Running"
	case store.RoutineQueued:
		return "Queued"
	case store.RoutineHeld:
		return "Held"
	case store.RoutineDone:
		return "Done"
	case store.RoutineLate:
		return "Ran late"
	case store.RoutineMissed:
		return "Missed"
	case store.RoutineSkipped:
		return "Skipped"
	case store.RoutineExpired:
		return "Approval expired"
	case store.RoutineFailed:
		return "Failed"
	default:
		if status == "" {
			return "Queued"
		}
		return status
	}
}

func (s *Server) secretNamesText(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	var rep report
	rep.line("Values stay in your vault. They are not shown.")
	if slot, ok := slotForModel(agent.Model); ok {
		sec, have, err := s.slotKey(ctx, user.ID, slot)
		if err != nil {
			return "Secrets could not be read.", true
		}
		sec.Value = ""
		if !have {
			rep.line("No model key is saved for this model.")
		} else {
			label := strings.TrimSpace(sec.Label)
			if label == "" {
				label = strings.TrimSpace(sec.Name)
			}
			if isServerKey(label) {
				label = serverKeyShort
			}
			rep.line("Model key: " + slot.Title + " key. " + label)
		}
	} else {
		rep.line("This model does not use a saved key.")
	}
	secrets, err := s.store.GrantedMeta(ctx, user.ID, agent.ID)
	if err != nil {
		return "Secrets could not be read.", true
	}
	if len(secrets) == 0 {
		rep.line("No agent secrets.")
	}
	agents, _ := s.store.ListAgents(ctx, user.ID)
	names := map[string]string{}
	for _, row := range agents {
		names[row.ID] = row.Name
	}
	for _, sec := range secrets {
		sec.Value = ""
		line := sec.Name
		if label := strings.TrimSpace(sec.Label); label != "" {
			line += ". " + label
		}
		rep.line(line)
		rep.line("Agent secret. Given " + monthDay(sec.CreatedAt, userLocation(user.Timezone)))
		if ids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID); err == nil && len(ids) > 0 {
			var who []string
			for _, id := range ids {
				if name := names[id]; name != "" {
					who = append(who, name)
				}
			}
			if len(who) > 0 {
				rep.line("Shared with " + strings.Join(who, ", "))
			}
		}
		if hist, err := s.historyRows(ctx, user, sec.ID, userLocation(user.Timezone)); err == nil {
			for _, row := range hist {
				rep.line(row.When + ". " + row.Text)
			}
		}
	}
	return rep.String(), false
}

func (s *Server) filesText(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	var rep report
	if agent.Status == "paused" {
		rep.line("Last-known workspace. The machine is paused, so this does not wake it.")
	}
	if s.diskLow() {
		rep.line("Server disk space is low.")
	}
	if n, err := s.store.UploadUsage(ctx, user.ID, user.WorkspaceID); err == nil {
		rep.line(formatBytes(n) + " of 2 GB")
		if n >= uploadQuota {
			rep.line("Uploads are at the 2 GB limit.")
		} else if n*10 >= uploadQuota*8 {
			rep.line("Uploads are past 80% of 2 GB.")
		}
	}
	cached, err := s.store.ListCached(ctx, user.ID, agent.ID)
	if err != nil {
		return "Files could not be read.", true
	}
	uploads, err := s.store.ListUploads(ctx, user.ID, agent.ID)
	if err != nil {
		return "Files could not be read.", true
	}
	held, err := s.store.ListHeldUploads(ctx, user.ID, agent.ID)
	if err != nil {
		return "Files could not be read.", true
	}
	if len(cached) == 0 && len(uploads) == 0 {
		rep.line("No files yet.")
		return rep.String(), false
	}
	if len(cached) > 0 {
		rep.line("Workspace")
		for _, row := range cached {
			rep.line(row.Name + ". " + formatBytes(row.Size))
		}
	}
	var ready []store.File
	for _, row := range uploads {
		if row.Status == "held" {
			continue
		}
		ready = append(ready, row)
	}
	if len(ready) > 0 {
		rep.line("Uploads")
		for _, row := range ready {
			line := row.Name + ". " + formatBytes(row.Size)
			if row.Path != "" {
				line += ". " + row.Path
			}
			rep.line(line)
		}
	}
	if len(held) > 0 {
		rep.line("Waiting to copy")
		for _, row := range held {
			rep.line(row.Name + ". saved in castbot, copied when the machine wakes.")
		}
	}
	return rep.String(), false
}

func (s *Server) connectionsText(ctx context.Context, user store.User) (string, bool) {
	var rep report
	for _, panel := range connect.Panels() {
		on, err := s.store.ConnectionSwitchedOn(ctx, user.ID, panel.ID)
		if err != nil {
			return "Connections could not be read.", true
		}
		state := "Not connected"
		if on {
			state = "Connected"
		}
		rep.line(panel.Title + ". " + state)
		levels, err := s.store.ConnectionLevels(ctx, user.ID, panel.ID)
		if err != nil {
			return "Connections could not be read.", true
		}
		for _, tool := range panel.Tools {
			level := tool.DefaultLevel()
			if lv := levels[tool.Name]; lv != "" {
				if norm, good := castcfg.NormalizeLevel(lv); good {
					level = norm
				}
			}
			rep.line(tool.Title + " (" + tool.Name + "): " + levelWord(level))
		}
		if panel.ID == connect.DigitalOcean {
			key, err := s.store.ConnectionSSHKey(ctx, user.ID, panel.ID)
			if err == nil && key.Fingerprint != "" {
				rep.line("SSH key fingerprint: " + key.Fingerprint + ". The private key stays in your vault.")
			}
		}
	}
	rep.line("A connection token stays in the vault. The agent never sees the value.")
	return rep.String(), false
}

func levelWord(level string) string {
	switch level {
	case "always-allow":
		return "Always allow"
	case "deny":
		return "Deny"
	case "ask":
		return "Ask"
	default:
		if level == "" {
			return "Ask"
		}
		return level
	}
}

func (s *Server) cliTokensText(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	rows, err := s.store.ListCLITokens(ctx, user.ID, agent.ID)
	if err != nil {
		return "Cast CLI tokens could not be read.", true
	}
	var rep report
	rep.line("The token is shown once to you. It is not included here.")
	if len(rows) == 0 {
		rep.line("No Cast CLI tokens yet.")
		return rep.String(), false
	}
	loc := userLocation(user.Timezone)
	for _, row := range rows {
		view := cliTokenView(row, loc)
		rep.line("Prefix: " + view.Prefix)
		rep.line("Created: " + view.Created)
		rep.line("Last used: " + view.LastUsed)
		rep.line("Status: " + view.Status)
	}
	return rep.String(), false
}

func (s *Server) accountOverviewText(ctx context.Context, user store.User, caller store.Agent) (string, bool) {
	var rep report
	rep.line("Name: " + user.DisplayName)
	workspace := strings.TrimSpace(user.WorkspaceName)
	if workspace == "" {
		workspace = "Personal"
	}
	rep.line("Workspace: " + workspace)
	rep.line("Theme: " + themeWord(user.Theme))
	zone := strings.TrimSpace(user.Timezone)
	if zone == "" {
		zone = "America/Denver"
	}
	rep.line("Timezone: " + zone)
	rep.line("Role: " + memberRole(user.Role))
	people, err := s.store.ListMembers(ctx, user.ID)
	if err != nil {
		return "The account could not be read.", true
	}
	rep.line("People")
	for _, person := range people {
		rep.line(person.DisplayName + ". " + memberRole(person.Role) + ". " + person.Email)
	}
	invites, err := s.store.ListInvites(ctx, user.ID)
	if err != nil {
		return "The account could not be read.", true
	}
	if len(invites) == 0 {
		rep.line("No pending invites.")
	} else {
		rep.line("Pending invites")
		for _, row := range invites {
			rep.line(row.DisplayName + ". " + row.Email)
		}
	}
	sections, err := s.store.ListSections(ctx, user.ID)
	if err != nil {
		return "The account could not be read.", true
	}
	if len(sections) > 0 {
		rep.line("Sections")
		for _, sec := range sections {
			rep.line(sec.Name)
		}
	}
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return "The account could not be read.", true
	}
	var others int
	for _, agent := range agents {
		if agent.ID == caller.ID {
			continue
		}
		if others == 0 {
			rep.line("Other agents")
		}
		others++
		line := agent.Name
		if agent.Title != "" {
			line += ". " + agent.Title
		}
		rep.line(line)
	}
	if others == 0 {
		rep.line("No other agents.")
	}
	groups, err := s.store.ListGroupCards(ctx, user.ID)
	if err != nil {
		return "The account could not be read.", true
	}
	var mine int
	for _, card := range groups {
		ok, err := s.store.IsParticipant(ctx, user.ID, card.ID, caller.ID)
		if err != nil || !ok {
			continue
		}
		if mine == 0 {
			rep.line("Groups")
		}
		mine++
		title := strings.TrimSpace(card.Title)
		if title == "" {
			title = "Group"
		}
		line := title
		if card.Pinned {
			line += ". Pinned"
		}
		if card.SectionID != "" {
			if sec, err := s.store.GetSection(ctx, user.ID, card.SectionID); err == nil && sec.Name != "" {
				line += ". " + sec.Name
			}
		}
		rep.line(line)
	}
	if mine == 0 {
		rep.line("This agent is not in a group.")
	}
	return rep.String(), false
}

func themeWord(theme string) string {
	switch strings.TrimSpace(theme) {
	case "dark":
		return "Dark"
	case "light":
		return "Light"
	default:
		return "System"
	}
}

func (s *Server) searchAccountText(ctx context.Context, user store.User, query string) (string, bool) {
	q := strings.TrimSpace(query)
	if q == "" || len(q) > 80 {
		return "Say what to search for.", true
	}
	low := strings.ToLower(q)
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return "Search could not be read.", true
	}
	var rep report
	var agentHits int
	for _, agent := range agents {
		if strings.Contains(strings.ToLower(agent.Name), low) || strings.Contains(strings.ToLower(agent.Title), low) {
			if agentHits == 0 {
				rep.line("Agents")
			}
			agentHits++
			line := agent.Name
			if agent.Title != "" {
				line += ". " + agent.Title
			}
			rep.line(line)
		}
	}
	hits, err := s.store.SearchFiles(ctx, user.ID, q)
	if err != nil {
		return "Search could not be read.", true
	}
	if len(hits) > 0 {
		rep.line("Files")
		for _, hit := range hits {
			line := hit.Name + ". " + hit.AgentName + ". " + formatBytes(hit.Size)
			if hit.Path != "" {
				line += ". " + hit.Path
			}
			if hit.Paused {
				line += ". paused"
			}
			rep.line(line)
		}
	}
	if agentHits == 0 && len(hits) == 0 {
		return "No matches.", false
	}
	return rep.String(), false
}
