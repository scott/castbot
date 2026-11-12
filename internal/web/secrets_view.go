package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/store"
)

// vaultView is the settings vault and the agent Secrets panel.
// It never carries a secret value.
type vaultView struct {
	Panel     bool
	Drawer    bool
	ModalOpen bool
	Watch     bool
	Phone     string
	Back      bool

	Summary vaultSummary
	Keys    []vaultKey
	Rows    []vaultRow
	Empty   bool

	Detail vaultDetail
	Form   vaultForm
	Pane   vaultPane
	Modal  vaultModal
}

type vaultSummary struct {
	Show        bool
	KeyTitle    string
	KeySub      string
	KeyMeta     string
	SecretLine  string
	HasKey      bool
	ReplaceHref string
}

type vaultKey struct {
	Slot, Title, Letter, Label, When, Used, UsedLabel, Href string
	Added, Idle                                             bool
	Avatars                                                 []vaultAv
}

type vaultAv struct {
	Letter, Color string
}

type vaultRow struct {
	ID, Name, When, Used, UsedLabel, Href string
	Avatars                               []vaultAv
	Unused, HL                            bool
}

type vaultDetail struct {
	ID, Name, KindLine, DeleteLine, Count string
	ShowRevoke                            bool
	Agents                                []vaultAgentRow
	History                               []vaultHist
	BannerClass, BannerLead, BannerRest   string
	ReloadHref, ReloadLabel               string
	Poll                                  bool
}

type vaultAgentRow struct {
	ID, Name, Sub, Letter, Color, Dot string
	Status, StatusText, RetryHref     string
	RevokeHref                        string
	Paused                            bool
}

type vaultHist struct {
	When, Text string
}

type vaultForm struct {
	Title, Action, Name, Label, Slot, SecretID string
	NameErr, ConflictHref, ValueErr            string
	Agents                                     []vaultPick
	Secrets                                    []vaultPick
	Note, Submit, Intro                        string
	Key                                        bool
}

type vaultPick struct {
	ID, Name, Sub, Letter, Color string
	Checked, Paused              bool
}

type vaultPane struct {
	Rows    []vaultPaneRow
	Fine    string
	AddHref string
}

type vaultPaneRow struct {
	Title, Sub string
	Model      bool
	RevokeHref string
}

type vaultModal struct {
	Class, Title, Body, Action, Close, Submit, SecretID string
	Lines                                               []string
	Green                                               string
	Value                                               bool
	Danger                                              bool
}

type secretEvent struct {
	Name    string `json:"name"`
	AgentID string `json:"agent_id,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Email   string `json:"email,omitempty"`
}

func englishAnd(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

func countPhrase(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Server) vaultSummary(ctx context.Context, user store.User) (vaultView, error) {
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return vaultView{}, err
	}
	loc := userLocation(user.Timezone)
	v := vaultView{Summary: vaultSummary{Show: true, SecretLine: "No secrets yet"}}
	rows, err := s.store.ListSecrets(ctx, user.ID, "*")
	if err != nil {
		return vaultView{}, err
	}
	granted := map[string]struct{}{}
	for _, row := range rows {
		ids, err := s.store.GrantAgentIDs(ctx, user.ID, row.ID)
		if err != nil {
			return vaultView{}, err
		}
		for _, id := range ids {
			granted[id] = struct{}{}
		}
	}
	if len(rows) > 0 {
		v.Summary.SecretLine = countPhrase(len(rows), "secret", "secrets") + ", given to " + countPhrase(len(granted), "agent", "agents")
	}
	for _, slot := range modelSlots() {
		key, ok, err := s.slotKey(ctx, user.ID, slot)
		if err != nil {
			return vaultView{}, err
		}
		if !ok {
			continue
		}
		users, err := s.agentsUsingSlot(ctx, user.ID, slot)
		if err != nil {
			return vaultView{}, err
		}
		label := key.Label
		if label == "" {
			label = "From the server"
		}
		v.Summary.HasKey = true
		v.Summary.KeyTitle = slot.Title
		v.Summary.KeySub = "Model key · " + label
		v.Summary.KeyMeta = "Used by " + countPhrase(len(users), "agent", "agents")
		if key.ID != "" {
			v.Summary.KeyMeta += " · added " + monthDay(key.CreatedAt, loc)
		}
		v.Summary.ReplaceHref = "/settings/secrets/item/" + key.ID
		if key.ID == "" {
			v.Summary.ReplaceHref = "/settings/secrets/keys/" + slot.ID
		}
		break
	}
	_ = agents
	return v, nil
}

// slotKey is the newest vault row for a slot, or an env-backed stand-in with an empty id.
func (s *Server) slotKey(ctx context.Context, userID string, slot keySlot) (store.Secret, bool, error) {
	rows, err := s.store.ListSecrets(ctx, userID, "")
	if err != nil {
		return store.Secret{}, false, err
	}
	var found *store.Secret
	for i := range rows {
		if rows[i].Provider != slot.ID && rows[i].Provider != slot.Manifest {
			continue
		}
		if found == nil || rows[i].CreatedAt > found.CreatedAt || (rows[i].CreatedAt == found.CreatedAt && rows[i].ID > found.ID) {
			cp := rows[i]
			found = &cp
		}
	}
	if found != nil {
		return *found, true, nil
	}
	if strings.TrimSpace(providerEnv(slot.Manifest)) != "" {
		return store.Secret{Name: slot.Env, Label: "From the server", Provider: slot.ID}, true, nil
	}
	return store.Secret{}, false, nil
}

func (s *Server) agentMap(ctx context.Context, userID string) ([]store.Agent, map[string]store.Agent, error) {
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	by := map[string]store.Agent{}
	for _, agent := range agents {
		by[agent.ID] = agent
	}
	return agents, by, nil
}

func (s *Server) agentPaused(ctx context.Context, userID string, agent store.Agent) bool {
	if agent.Status == "paused" {
		return true
	}
	vm, err := s.store.LiveVM(ctx, userID, agent.ID)
	return err == nil && vm.State == "paused"
}

func vaultSub(agent store.Agent) string {
	_, _, label, _ := statusBits(agent)
	if agent.Status == "failed" {
		label = "Machine failed"
	}
	if agent.Title == "" {
		return label
	}
	return agent.Title + " · " + label
}

func avatars(agents []store.Agent, limit int) []vaultAv {
	var out []vaultAv
	for _, agent := range agents {
		if len(out) >= limit {
			break
		}
		out = append(out, vaultAv{Letter: letter(agent.Name), Color: colorOr(agent.Color)})
	}
	return out
}

func colorOr(c string) string {
	if c == "" {
		return "c0"
	}
	return c
}

func usedLabel(names []string) (short, full string) {
	if len(names) == 0 {
		return "", ""
	}
	full = "Used by " + englishAnd(names)
	if len(names) <= 2 {
		return full, full
	}
	return fmt.Sprintf("Used by %s, %s +%d", names[0], names[1], len(names)-2), full
}

func (s *Server) namesOf(agents []store.Agent, ids []string) []store.Agent {
	by := map[string]store.Agent{}
	for _, agent := range agents {
		by[agent.ID] = agent
	}
	var out []store.Agent
	seen := map[string]bool{}
	for _, agent := range agents {
		if seen[agent.ID] {
			continue
		}
		for _, id := range ids {
			if id == agent.ID {
				out = append(out, agent)
				seen[agent.ID] = true
			}
		}
	}
	_ = by
	return out
}

func (s *Server) listView(r *http.Request, user store.User, form vaultForm, highlight string) (vaultView, error) {
	ctx := r.Context()
	agents, _, err := s.agentMap(ctx, user.ID)
	if err != nil {
		return vaultView{}, err
	}
	loc := userLocation(user.Timezone)
	v := vaultView{Phone: "Secrets"}
	rows, err := s.store.ListSecrets(ctx, user.ID, "")
	if err != nil {
		return vaultView{}, err
	}
	for _, slot := range modelSlots() {
		key := vaultKey{Slot: slot.ID, Title: slot.Title, Letter: slot.Letter, Href: "/settings/secrets/keys/" + slot.ID}
		sec, ok, err := s.slotKey(ctx, user.ID, slot)
		effectiveID := ""
		if err != nil {
			return vaultView{}, err
		}
		if ok {
			users, err := s.agentsUsingSlot(ctx, user.ID, slot)
			if err != nil {
				return vaultView{}, err
			}
			names := make([]string, len(users))
			for i := range users {
				names[i] = users[i].Name
			}
			short, full := usedLabel(names)
			label := sec.Label
			if label == "" {
				label = sec.Name
			}
			key.Added = true
			key.Label = label
			key.When = "Added " + monthDay(sec.CreatedAt, loc)
			if sec.ID == "" {
				key.When = "From the server"
			}
			key.Used = short
			key.UsedLabel = full
			key.Avatars = avatars(users, 3)
			if sec.ID != "" {
				key.Href = "/settings/secrets/item/" + sec.ID + "/replace"
				effectiveID = sec.ID
			}
		}
		v.Keys = append(v.Keys, key)
		for _, extra := range rows {
			if extra.ID == "" || extra.ID == effectiveID {
				continue
			}
			if extra.Provider != slot.ID && extra.Provider != slot.Manifest {
				continue
			}
			label := extra.Label
			if label == "" {
				label = extra.Name
			}
			v.Keys = append(v.Keys, vaultKey{
				Slot: slot.ID, Title: slot.Title, Letter: slot.Letter,
				Label: label, Added: true, Idle: true,
				When: "Added " + monthDay(extra.CreatedAt, loc),
				Href: "/settings/secrets/item/" + extra.ID + "/delete",
			})
		}
	}
	for _, sec := range rows {
		if sec.Provider != "" {
			continue
		}
		ids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
		if err != nil {
			return vaultView{}, err
		}
		got := s.namesOf(agents, ids)
		names := make([]string, len(got))
		for i := range got {
			names[i] = got[i].Name
		}
		short, full := usedLabel(names)
		when := "Added " + monthDay(sec.CreatedAt, loc)
		if sec.RotatedAt != "" {
			when += " · replaced " + monthDay(sec.RotatedAt, loc)
		}
		row := vaultRow{
			ID: sec.ID, Name: sec.Name, When: when, Used: short, UsedLabel: full,
			Href: "/settings/secrets/item/" + sec.ID, Avatars: avatars(got, 3),
			Unused: len(got) == 0, HL: sec.ID == highlight,
		}
		v.Rows = append(v.Rows, row)
	}
	v.Empty = len(v.Rows) == 0
	if form.Title != "" {
		v.Drawer = true
		v.Form = form
		if v.Form.Note == "" {
			v.Form.Note = grantNote(form.Agents)
		}
	}
	return v, nil
}

func grantNote(picks []vaultPick) string {
	var running, paused []string
	for _, pick := range picks {
		if !pick.Checked {
			continue
		}
		if pick.Paused {
			paused = append(paused, pick.Name)
		} else {
			running = append(running, pick.Name)
		}
	}
	var parts []string
	if len(running) > 0 {
		parts = append(parts, englishAnd(running)+" reload to get it. No new machine.")
	}
	switch len(paused) {
	case 0:
	case 1:
		parts = append(parts, paused[0]+" gets it when it wakes.")
	default:
		parts = append(parts, englishAnd(paused)+" get it when they wake.")
	}
	if len(parts) == 0 {
		return "No agent gets it yet."
	}
	return strings.Join(parts, " ")
}

func (s *Server) picks(ctx context.Context, userID string, checked map[string]bool) ([]vaultPick, error) {
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []vaultPick
	for _, agent := range agents {
		paused := s.agentPaused(ctx, userID, agent)
		out = append(out, vaultPick{
			ID: agent.ID, Name: agent.Name, Sub: vaultSub(agent),
			Letter: letter(agent.Name), Color: colorOr(agent.Color),
			Checked: checked[agent.ID], Paused: paused,
		})
	}
	return out, nil
}

func checkedSet(r *http.Request) map[string]bool {
	out := map[string]bool{}
	for _, id := range r.Form["agent"] {
		out[id] = true
	}
	if len(out) == 0 {
		for _, id := range r.URL.Query()["agent"] {
			out[id] = true
		}
	}
	return out
}

func (s *Server) vaultItem(r *http.Request, user store.User, id string) (vaultView, error) {
	ctx := r.Context()
	sec, err := s.store.SecretMeta(ctx, user.ID, id)
	if err != nil {
		return vaultView{}, err
	}
	agents, _, err := s.agentMap(ctx, user.ID)
	if err != nil {
		return vaultView{}, err
	}
	loc := userLocation(user.Timezone)
	d := vaultDetail{ID: sec.ID, Name: sec.Name, ShowRevoke: sec.Provider == ""}
	if sec.Provider != "" {
		slot, _ := slotByID(sec.Provider)
		title := slot.Title
		if title == "" {
			title = "Model"
		}
		d.KindLine = "Model key · added " + monthDay(sec.CreatedAt, loc) + " by you"
		if sec.Label != "" {
			d.Name = sec.Label
		}
	} else {
		d.KindLine = "Agent secret · added " + monthDay(sec.CreatedAt, loc) + " by you"
		if sec.RotatedAt != "" {
			d.KindLine += " · replaced " + monthDay(sec.RotatedAt, loc)
		}
	}
	var granted []store.Agent
	if sec.Provider == "" {
		ids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
		if err != nil {
			return vaultView{}, err
		}
		granted = s.namesOf(agents, ids)
	} else if slot, ok := slotByID(sec.Provider); ok {
		key, have, err := s.slotKey(ctx, user.ID, slot)
		if err != nil {
			return vaultView{}, err
		}
		if have && key.ID == sec.ID {
			granted, err = s.agentsUsingSlot(ctx, user.ID, slot)
			if err != nil {
				return vaultView{}, err
			}
		}
	}
	d.Count = countPhrase(len(granted), "agent", "agents")
	names := make([]string, len(granted))
	for i, agent := range granted {
		names[i] = agent.Name
		row := s.agentRow(ctx, user, sec, agent)
		d.Agents = append(d.Agents, row)
	}
	if len(names) == 0 {
		d.DeleteLine = "Removes the value for good."
	} else if sec.Provider != "" {
		d.DeleteLine = "Removes the value for good. Agents that use this model lose the key."
	} else {
		d.DeleteLine = "Removes the value for good and takes it away from " + englishAnd(names) + "."
	}
	d.History, err = s.historyRows(ctx, user, sec.ID, loc)
	if err != nil {
		return vaultView{}, err
	}
	s.applyReload(user, sec, &d)
	v := vaultView{Phone: d.Name, Back: true, Detail: d, Watch: r.URL.Query().Get("watch") == "1" || d.Poll}
	if q := r.URL.Query().Get("modal"); q != "" {
		if err := s.attachModal(ctx, user, sec, granted, q, r.URL.Query().Get("agent"), &v); err != nil {
			return vaultView{}, err
		}
	}
	if r.URL.Query().Get("give") == "1" && sec.Provider == "" {
		form, err := s.giveAgentsForm(ctx, user, sec, granted)
		if err != nil {
			return vaultView{}, err
		}
		v.Drawer = true
		v.Form = form
	}
	return v, nil
}

func (s *Server) agentRow(ctx context.Context, user store.User, sec store.Secret, agent store.Agent) vaultAgentRow {
	_, class, _, _ := statusBits(agent)
	row := vaultAgentRow{
		ID: agent.ID, Name: agent.Name, Sub: vaultSub(agent),
		Letter: letter(agent.Name), Color: colorOr(agent.Color), Dot: class,
		Paused: s.agentPaused(ctx, user.ID, agent),
	}
	if sec.Provider == "" {
		row.RevokeHref = "/settings/secrets/item/" + sec.ID + "?modal=revoke&agent=" + agent.ID
	}
	note, ok := s.reloadNote(user.ID, agent.ID)
	if ok && (note.SecretID == "" || note.SecretID == sec.ID) {
		row.Status = note.Status
		switch note.Status {
		case "reloaded":
			row.StatusText = "Reloaded " + clockLabel(note.At.UTC().Format(timeRFC3339), userLocation(user.Timezone))
		case "deferred":
			row.StatusText = "Reloads after its reply"
		case "wake":
			row.StatusText = "On next wake"
		case "failed":
			row.StatusText = "Did not reload"
			row.RetryHref = "/settings/secrets/item/" + sec.ID + "/reload/" + agent.ID
		}
	}
	return row
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func (s *Server) applyReload(user store.User, sec store.Secret, d *vaultDetail) {
	var now, done, fail, wake []string
	for _, row := range d.Agents {
		switch row.Status {
		case "reloaded":
			done = append(done, row.Name)
			now = append(now, row.Name)
		case "deferred":
			now = append(now, row.Name)
			d.Poll = true
		case "failed":
			fail = append(fail, row.Name)
			now = append(now, row.Name)
		case "wake":
			wake = append(wake, row.Name)
		}
	}
	if len(now) == 0 && len(wake) == 0 {
		return
	}
	if len(fail) > 0 {
		d.BannerClass = "err"
		d.BannerLead = "The new value is saved, but " + englishAnd(fail) + " did not reload."
		rest := "Its machine did not answer. "
		keep := "keeps"
		if len(fail) > 1 {
			rest = "Their machines did not answer. "
			keep = "keep"
		}
		rest += englishAnd(fail) + " " + keep + " using the old value until it reloads."
		if len(fail) > 1 {
			rest = strings.Replace(rest, "until it reloads.", "until they reload.", 1)
		}
		if len(done) == 1 {
			rest += " " + done[0] + " has the new one."
		} else if len(done) > 1 {
			rest += " " + englishAnd(done) + " have the new one."
		}
		d.BannerRest = rest
		if len(fail) == 1 {
			d.ReloadLabel = "Reload " + fail[0]
			d.ReloadHref = "/settings/secrets/item/" + sec.ID + "/reload/" + agentIDByName(d.Agents, fail[0])
		}
		return
	}
	pending := len(now) > len(done)
	if pending {
		word := "agents"
		if len(now) == 1 {
			word = "agent"
		}
		d.BannerClass = "start"
		d.BannerLead = fmt.Sprintf("New value saved. Reloading %d %s.", len(now), word)
		d.BannerRest = "This updates on its own."
		d.Poll = true
		return
	}
	if len(done) > 0 || len(wake) > 0 {
		var parts []string
		if len(done) > 0 {
			parts = append(parts, englishAnd(done)+" reloaded.")
		}
		switch len(wake) {
		case 1:
			parts = append(parts, wake[0]+" gets it when it wakes.")
		default:
			if len(wake) > 1 {
				parts = append(parts, englishAnd(wake)+" get it when they wake.")
			}
		}
		parts = append(parts, "No new machines.")
		d.BannerClass = "ok"
		d.BannerLead = sec.Name + " replaced."
		d.BannerRest = strings.Join(parts, " ")
	}
}

func agentIDByName(rows []vaultAgentRow, name string) string {
	for _, row := range rows {
		if row.Name == name {
			return row.ID
		}
	}
	return ""
}

func (s *Server) historyRows(ctx context.Context, user store.User, secretID string, loc interface{ String() string }) ([]vaultHist, error) {
	rows, err := s.store.SecretAudit(ctx, user.ID, secretID)
	if err != nil {
		return nil, err
	}
	zone := userLocation(user.Timezone)
	var out []vaultHist
	for _, row := range rows {
		var ev secretEvent
		_ = json.Unmarshal([]byte(row.Detail), &ev)
		text := ""
		switch row.Action {
		case "secret.added":
			if ev.Agent != "" {
				text = "Added and given to " + ev.Agent
			} else {
				text = "Added"
			}
		case "secret.granted":
			text = "Given to " + ev.Agent
		case "secret.replaced":
			who := ev.Email
			if who == "" {
				who = user.Email
			}
			text = "Value replaced by " + who
		case "secret.revoked":
			text = "Taken away from " + ev.Agent
		default:
			continue
		}
		out = append(out, vaultHist{When: clockStamp(row.CreatedAt, zone), Text: text})
	}
	return out, nil
}

func (s *Server) attachModal(ctx context.Context, user store.User, sec store.Secret, granted []store.Agent, kind, focus string, v *vaultView) error {
	var now, wake []string
	for _, agent := range granted {
		if s.agentPaused(ctx, user.ID, agent) {
			wake = append(wake, agent.Name)
		} else {
			now = append(now, agent.Name)
		}
	}
	switch kind {
	case "replace":
		m := vaultModal{
			Class: "acc", Title: "Replace " + sec.Name + "?",
			Body:   "The old value is overwritten. castbot cannot bring it back.",
			Action: "/settings/secrets/item/" + sec.ID + "/replace", Submit: "Replace and reload",
			Value: true, Green: "No new machine. Chats, memory and files stay as they are.",
		}
		if len(now) > 0 {
			m.Lines = append(m.Lines, englishAnd(now)+" reload now. If one is replying, it reloads when the reply ends.")
		}
		switch len(wake) {
		case 1:
			m.Lines = append(m.Lines, wake[0]+" is paused. It gets the new value the next time it wakes.")
		default:
			if len(wake) > 1 {
				m.Lines = append(m.Lines, englishAnd(wake)+" are paused. They get the new value the next time they wake.")
			}
		}
		v.Modal = m
		v.ModalOpen = true
	case "delete":
		if sec.Provider != "" {
			if slot, ok := slotByID(sec.Provider); ok {
				blocked, err := s.keyInUse(ctx, user.ID, sec, slot)
				if err != nil {
					return err
				}
				if blocked != "" {
					v.Modal = vaultModal{
						Class: "warnm", Title: "This key is in use", Body: blocked,
						Submit: "Replace", SecretID: sec.ID,
						Action: "/settings/secrets/item/" + sec.ID + "/replace",
						Close:  "/settings/secrets",
					}
					v.ModalOpen = true
					return nil
				}
			}
		}
		m := vaultModal{
			Title:  "Delete " + sec.Name + "?",
			Body:   "This removes the value for good. castbot cannot bring it back.",
			Action: "/settings/secrets/item/" + sec.ID + "/delete", Submit: "Delete secret", Danger: true,
		}
		if len(now) > 0 {
			m.Lines = append(m.Lines, englishAnd(now)+" lose it and reload now. No new machine.")
		}
		switch len(wake) {
		case 1:
			m.Lines = append(m.Lines, wake[0]+" loses it the next time it wakes.")
		default:
			if len(wake) > 1 {
				m.Lines = append(m.Lines, englishAnd(wake)+" lose it the next time they wake.")
			}
		}
		if len(granted) > 0 {
			m.Lines = append(m.Lines, "Tools that need "+sec.Name+" stop working for these agents.")
		}
		v.Modal = m
		v.ModalOpen = true
	case "revoke":
		var target store.Agent
		var others []string
		for _, agent := range granted {
			if agent.ID == focus {
				target = agent
				continue
			}
			others = append(others, agent.Name)
		}
		if target.ID == "" {
			return store.ErrNotFound
		}
		body := target.Name + " reloads without it, so tools that need " + sec.Name + " stop working for " + target.Name + ". No new machine. The secret stays in your vault"
		if s.agentPaused(ctx, user.ID, target) {
			body = target.Name + " is paused. It loses " + sec.Name + " the next time it wakes. No new machine. The secret stays in your vault"
		}
		if len(others) > 0 {
			body += ", and " + englishAnd(others) + " keep it."
		} else {
			body += "."
		}
		v.Modal = vaultModal{
			Class: "acc", Title: "Take " + sec.Name + " away from " + target.Name + "?",
			Body: body, Action: "/agents/" + target.ID + "/secrets/revoke",
			Submit: "Revoke and reload", SecretID: sec.ID,
		}
		v.ModalOpen = true
	}
	return nil
}

func (s *Server) keyInUse(ctx context.Context, userID string, sec store.Secret, slot keySlot) (string, error) {
	key, ok, err := s.slotKey(ctx, userID, slot)
	if err != nil || !ok || key.ID != sec.ID {
		return "", err
	}
	users, err := s.agentsUsingSlot(ctx, userID, slot)
	if err != nil || len(users) == 0 {
		return "", err
	}
	names := make([]string, len(users))
	for i := range users {
		names[i] = users[i].Name
	}
	label := sec.Label
	if label == "" {
		label = sec.Name
	}
	return label + " is the " + slot.Title + " key for " + englishAnd(names) + ". Replace this key, or change those agents' model first.", nil
}

func (s *Server) giveAgentsForm(ctx context.Context, user store.User, sec store.Secret, granted []store.Agent) (vaultForm, error) {
	have := map[string]bool{}
	for _, agent := range granted {
		have[agent.ID] = true
	}
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return vaultForm{}, err
	}
	form := vaultForm{
		Title:  "Give " + sec.Name + " to another agent",
		Action: "/settings/secrets/item/" + sec.ID + "/grant",
		Submit: "Give and reload", SecretID: sec.ID,
		Intro: "Only your agents are listed. You can change this later.",
	}
	for _, agent := range agents {
		if have[agent.ID] {
			continue
		}
		form.Agents = append(form.Agents, vaultPick{
			ID: agent.ID, Name: agent.Name, Sub: vaultSub(agent),
			Letter: letter(agent.Name), Color: colorOr(agent.Color),
			Paused: s.agentPaused(ctx, user.ID, agent),
		})
	}
	form.Note = grantNote(form.Agents)
	return form, nil
}

func (s *Server) fillAgentSecrets(r *http.Request, user store.User, agent store.Agent, p *page) error {
	v, err := s.panelView(r, user, agent)
	if err != nil {
		return err
	}
	p.Vault = v
	return nil
}

func (s *Server) panelView(r *http.Request, user store.User, agent store.Agent) (vaultView, error) {
	ctx := r.Context()
	v := vaultView{Panel: true}
	v.Pane.Fine = agent.Name + " reads these as environment variables. Adding, replacing or revoking one reloads " + agent.Name + ", with no new machine. The model key comes with the agent's model. Values stay in your vault in Settings, Secrets."
	v.Pane.AddHref = "/agents/" + agent.ID + "?panel=secrets&give=1"
	if slot, ok := slotForModel(agent.Model); ok {
		sec, have, err := s.slotKey(ctx, user.ID, slot)
		if err != nil {
			return vaultView{}, err
		}
		if have {
			label := sec.Label
			if label == "" {
				label = "From the server"
			}
			v.Pane.Rows = append(v.Pane.Rows, vaultPaneRow{Title: slot.Title + " key", Sub: "Model key · " + label, Model: true})
		}
	}
	secrets, err := s.store.GrantedMeta(ctx, user.ID, agent.ID)
	if err != nil {
		return vaultView{}, err
	}
	for _, sec := range secrets {
		v.Pane.Rows = append(v.Pane.Rows, vaultPaneRow{
			Title: sec.Name, Sub: "Agent secret · given " + monthDay(sec.CreatedAt, userLocation(user.Timezone)),
			RevokeHref: "/agents/" + agent.ID + "?panel=secrets&revoke=" + sec.ID,
		})
	}
	if id := r.URL.Query().Get("revoke"); id != "" {
		sec, err := s.store.SecretMeta(ctx, user.ID, id)
		if err != nil {
			return vaultView{}, err
		}
		ids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
		if err != nil {
			return vaultView{}, err
		}
		agents, _, err := s.agentMap(ctx, user.ID)
		if err != nil {
			return vaultView{}, err
		}
		var others []string
		for _, got := range s.namesOf(agents, ids) {
			if got.ID != agent.ID {
				others = append(others, got.Name)
			}
		}
		body := agent.Name + " reloads without it, so tools that need " + sec.Name + " stop working for " + agent.Name + ". No new machine. The secret stays in your vault"
		if s.agentPaused(ctx, user.ID, agent) {
			body = agent.Name + " is paused. It loses " + sec.Name + " the next time it wakes. No new machine. The secret stays in your vault"
		}
		if len(others) > 0 {
			body += ", and " + englishAnd(others) + " keep it."
		} else {
			body += "."
		}
		v.ModalOpen = true
		v.Modal = vaultModal{
			Class: "acc", Title: "Take " + sec.Name + " away from " + agent.Name + "?",
			Body: body, Action: "/agents/" + agent.ID + "/secrets/revoke",
			Submit: "Revoke and reload", SecretID: sec.ID,
		}
	}
	if r.URL.Query().Get("give") == "1" {
		form, err := s.giveSecretsForm(ctx, user, agent)
		if err != nil {
			return vaultView{}, err
		}
		v.Drawer = true
		v.Form = form
	}
	return v, nil
}

func (s *Server) giveSecretsForm(ctx context.Context, user store.User, agent store.Agent) (vaultForm, error) {
	have := map[string]bool{}
	ids, err := s.store.GrantedMeta(ctx, user.ID, agent.ID)
	if err != nil {
		return vaultForm{}, err
	}
	for _, sec := range ids {
		have[sec.ID] = true
	}
	rows, err := s.store.ListSecrets(ctx, user.ID, "*")
	if err != nil {
		return vaultForm{}, err
	}
	all, _, err := s.agentMap(ctx, user.ID)
	if err != nil {
		return vaultForm{}, err
	}
	form := vaultForm{
		Title:  "Give " + agent.Name + " a secret",
		Intro:  "Your secrets that " + agent.Name + " does not have yet. " + agent.Name + " gets each one under its name, as an environment variable.",
		Action: "/agents/" + agent.ID + "/secrets/grant",
		Submit: "Give to " + agent.Name,
	}
	if s.agentPaused(ctx, user.ID, agent) {
		form.Note = agent.Name + " gets it when it wakes. No new machine."
	} else {
		form.Note = agent.Name + " reloads to get it. No new machine."
	}
	for _, sec := range rows {
		if have[sec.ID] {
			continue
		}
		gids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
		if err != nil {
			return vaultForm{}, err
		}
		got := s.namesOf(all, gids)
		sub := "Unused"
		if len(got) == 1 {
			sub = "Used by " + got[0].Name
		} else if len(got) > 1 {
			names := make([]string, len(got))
			for i := range got {
				names[i] = got[i].Name
			}
			sub, _ = usedLabel(names)
		}
		form.Secrets = append(form.Secrets, vaultPick{ID: sec.ID, Name: sec.Name, Sub: sub})
	}
	return form, nil
}

func (s *Server) renderVault(w http.ResponseWriter, r *http.Request, user store.User, status int, v vaultView) {
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "secrets"
	p.Title = "Settings"
	if v.Back {
		p.Title = v.Phone
	}
	p.Vault = v
	s.render(w, status, "app", finish(p))
}

func (s *Server) audit(ctx context.Context, user store.User, action, secretID, name, agentID, agentName string) {
	_ = s.store.RecordSecretAudit(ctx, user.ID, action, secretID, secretEvent{
		Name: name, AgentID: agentID, Agent: agentName, Email: user.Email,
	})
}
