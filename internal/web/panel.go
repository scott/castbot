package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
)

const toastCookie = "castbot_toasted"

// glanceRow is one "At a glance" link on the Agent tab.
type glanceRow struct {
	Title, Sub, Href string
}

// earlierRow is a settled or CLI approval in the Approvals tab.
type earlierRow struct {
	Tool, Line, When string
	OK               bool
}

// toastView is one approval toast for an agent the person is not viewing.
type toastView struct {
	AgentName, Tool, Href string
}

func requestedPanel(r *http.Request) string {
	switch r.URL.Query().Get("panel") {
	case "agent", "machine", "secrets", "approvals", "routines", "files":
		return r.URL.Query().Get("panel")
	}
	if r.URL.Query().Get("pane") == "1" {
		return "agent"
	}
	return ""
}

func panelPartial(r *http.Request) bool {
	if r.Header.Get("HX-Request") != "true" {
		return false
	}
	switch r.Header.Get("HX-Target") {
	case "agent-panel", "#agent-panel":
		return true
	default:
		return false
	}
}

// outsidePanelChrome is true when this response also paints a drawer or modal
// that lives outside #agent-panel. A panel swap cannot show that chrome.
func outsidePanelChrome(p page) bool {
	return p.Vault.Drawer || p.Vault.ModalOpen || p.Routines.Delete.On || p.Replace.Confirm
}

func panelRedirect(r *http.Request) string {
	if r.URL.RequestURI() != "" {
		return r.URL.RequestURI()
	}
	loc := r.URL.Path
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	return loc
}

func panelTemplate(p page) string {
	switch p.Panel {
	case "files":
		return "filesPane"
	case "routines":
		return "routinesPane"
	case "secrets":
		return "secretPanel"
	case "machine":
		return "machinePane"
	case "approvals":
		return "approvalsPane"
	default:
		return "agentPane"
	}
}

func (s *Server) redirectLegacy(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if r.URL.Path == "/usage" {
		http.Redirect(w, r, "/settings#usage", http.StatusFound)
		return
	}
	panel := "approvals"
	switch r.URL.Path {
	case "/routines":
		panel = "routines"
	case "/files":
		panel = "files"
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil || len(agents) == 0 {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	id := mostRecentAgent(agents).ID
	if panel == "approvals" {
		if rows, err := s.store.ListPendingApprovals(r.Context(), user.ID); err == nil && len(rows) > 0 {
			id = rows[0].AgentID
		}
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(id)+"?panel="+panel, http.StatusFound)
}

func (s *Server) redirectAgentPanel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	panel := "machine"
	switch {
	case strings.HasSuffix(r.URL.Path, "/routines"):
		panel = "routines"
	case strings.HasSuffix(r.URL.Path, "/files"):
		panel = "files"
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"?panel="+panel, http.StatusFound)
}

func mostRecentAgent(agents []store.Agent) store.Agent {
	best := agents[0]
	for _, a := range agents[1:] {
		if a.CreatedAt > best.CreatedAt || (a.CreatedAt == best.CreatedAt && a.ID > best.ID) {
			best = a
		}
	}
	return best
}

func (s *Server) applyPanel(w http.ResponseWriter, r *http.Request, user store.User, agent store.Agent, p *page) bool {
	panel := requestedPanel(r)
	if wantsConfirm(r) {
		panel = "machine"
	}
	if p.Panel == "files" {
		panel = "files"
	}
	if panel == "" && p.Vault.Panel {
		panel = "secrets"
	}
	if panel == "" && p.Routines.On {
		panel = "routines"
	}
	if panel != "" {
		p.Panel = panel
		p.Pane = true
		if !strings.HasSuffix(r.URL.Path, "/machine/card") {
			p.Page = "chat"
			p.Tab = "chat"
		}
	}
	switch panel {
	case "agent":
		s.fillGlance(r, user, agent, p)
	case "approvals":
		s.fillApprovals(r, user, agent, p)
	case "secrets":
		if !p.Vault.Panel {
			p.Vault.Panel = true
			if err := s.fillAgentSecrets(r, user, agent, p); err != nil {
				if !s.missing(w, err) {
					http.Error(w, "Error", http.StatusInternalServerError)
				}
				return false
			}
		}
	}
	if p.HasAgent {
		p.OtherApprovals = p.Approvals - p.Agent.Approvals
		if p.OtherApprovals < 0 {
			p.OtherApprovals = 0
		}
	}
	if op := r.URL.Query().Get("opener"); op == "replace-open" || op == "replace-hint" {
		p.Replace.Opener = op
	}
	restoreNeeds(p)
	return true
}

func restoreNeeds(p *page) {
	fix := func(a *agentView) {
		if a.Approvals > 0 {
			a.Sub = joinDot(a.Title, "needs you")
			a.SubClass = ""
		}
	}
	fix(&p.Agent)
	for i := range p.Sections {
		for j := range p.Sections[i].Agents {
			fix(&p.Sections[i].Agents[j])
		}
	}
}

func (s *Server) fillGlance(r *http.Request, user store.User, agent store.Agent, p *page) {
	loc := userLocation(user.Timezone)
	now := s.store.Now()
	base := "/agents/" + url.PathEscape(agent.ID)
	p.ModelTitle, p.ModelSub = s.modelLine(r, user.ID, agent.Model)
	machineSub := p.Agent.ChipLabel
	if machineSub == "" {
		machineSub = "No machine"
	}
	if p.Replace.Update {
		machineSub += " · update available"
	}
	rows := []glanceRow{{
		Title: "Machine", Sub: machineSub, Href: base + "?panel=machine",
	}}
	if agent.Approvals > 0 {
		title := "1 approval waiting"
		if agent.Approvals != 1 {
			title = fmt.Sprintf("%d approvals waiting", agent.Approvals)
		}
		sub := "Open"
		if list, err := s.store.ListAgentApprovals(r.Context(), user.ID, agent.ID); err == nil {
			var pending []store.Approval
			for _, row := range list {
				if row.Status == "pending" && row.Origin == "web" {
					pending = append(pending, row)
				}
			}
			if len(pending) > 0 {
				tool := pending[0].Tool
				if tool == "" {
					tool = "a tool"
				}
				sub = tool
				if age := relAge(pending[0].CreatedAt, now); age != "" {
					sub += " · " + age
				}
				if len(pending) > 1 {
					sub = fmt.Sprintf("%s and %d more", tool, len(pending)-1)
				}
			}
		}
		rows = append(rows, glanceRow{Title: title, Sub: sub, Href: base + "?panel=approvals"})
	} else {
		rows = append(rows, glanceRow{Title: "Approvals", Sub: "Nothing waiting", Href: base + "?panel=approvals"})
	}
	secretTitle, secretSub := s.secretGlance(r, user.ID, agent)
	rows = append(rows, glanceRow{Title: secretTitle, Sub: secretSub, Href: base + "?panel=secrets"})
	routineTitle, routineSub := s.routineGlance(r, user, agent, now, loc)
	rows = append(rows, glanceRow{Title: routineTitle, Sub: routineSub, Href: base + "?panel=routines"})
	fileTitle, fileSub := s.fileGlance(r, user, agent, loc)
	rows = append(rows, glanceRow{Title: fileTitle, Sub: fileSub, Href: base + "?panel=files"})
	p.Glance = rows
}

func (s *Server) modelLine(r *http.Request, userID, model string) (string, string) {
	if strings.TrimSpace(model) == "" {
		return "No model saved", "Keys stay on your account."
	}
	sub := "Keys stay on your account."
	if slot, ok := slotForModel(model); ok {
		if sec, have, err := s.slotKey(r.Context(), userID, slot); err == nil && have {
			label := sec.Label
			if label == "" {
				label = sec.Name
			}
			if label == "From the server" {
				sub = "From the server"
			} else if label != "" {
				sub = "From your vault · " + label
			}
		}
	}
	return model, sub
}

func (s *Server) secretGlance(r *http.Request, userID string, agent store.Agent) (string, string) {
	n := 0
	var names []string
	if rows, err := s.store.GrantedMeta(r.Context(), userID, agent.ID); err == nil {
		for _, row := range rows {
			n++
			if row.Name != "" {
				names = append(names, row.Name)
			}
		}
	}
	if slot, ok := slotForModel(agent.Model); ok {
		if sec, have, err := s.slotKey(r.Context(), userID, slot); err == nil && have {
			n++
			label := sec.Label
			if label == "" || label == "From the server" {
				label = sec.Name
			}
			if label != "" && label != "From the server" {
				names = append([]string{label}, names...)
			}
		}
	}
	title := "No secrets"
	if n == 1 {
		title = "1 secret"
	} else if n > 1 {
		title = fmt.Sprintf("%d secrets", n)
	}
	sub := "Values stay in your vault."
	switch len(names) {
	case 0:
	case 1:
		sub = "From your vault · " + names[0]
	default:
		sub = fmt.Sprintf("From your vault · %s and %d more", names[0], len(names)-1)
	}
	return title, sub
}

func (s *Server) routineGlance(r *http.Request, user store.User, agent store.Agent, now time.Time, loc *time.Location) (string, string) {
	rows, err := s.store.ListRoutines(r.Context(), user.ID, agent.ID)
	if err != nil {
		return "Routines", "Open"
	}
	n := 0
	var next time.Time
	for _, row := range rows {
		if row.DeletedAt != "" {
			continue
		}
		n++
		if !row.Enabled || row.NextRunAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, row.NextRunAt)
		if err != nil {
			continue
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	title := "No routines"
	if n == 1 {
		title = "1 routine"
	} else if n > 1 {
		title = fmt.Sprintf("%d routines", n)
	}
	sub := "Nothing scheduled"
	if !next.IsZero() {
		sub = "Next: " + next.In(loc).Format("Mon 3:04 PM")
	}
	_ = now
	return title, sub
}

func (s *Server) fileGlance(r *http.Request, user store.User, agent store.Agent, loc *time.Location) (string, string) {
	n := 0
	var latest time.Time
	name := ""
	if rows, err := s.store.ListCached(r.Context(), user.ID, agent.ID); err == nil {
		for _, row := range rows {
			n++
			if row.SeenAt.After(latest) {
				latest = row.SeenAt
				name = row.Name
			}
		}
	}
	if rows, err := s.store.ListUploads(r.Context(), user.ID, agent.ID); err == nil {
		for _, row := range rows {
			n++
			when := row.CreatedAt
			if row.SeenAt.After(when) {
				when = row.SeenAt
			}
			if when.After(latest) {
				latest = when
				name = row.Name
			}
		}
	}
	title := "No files"
	if n == 1 {
		title = "1 file"
	} else if n > 1 {
		title = fmt.Sprintf("%d files", n)
	}
	sub := "None yet"
	if name != "" && !latest.IsZero() {
		sub = "Latest: " + name + ", " + latest.In(loc).Format("3:04 PM")
	}
	return title, sub
}

func (s *Server) fillApprovals(r *http.Request, user store.User, agent store.Agent, p *page) {
	rows, err := s.store.ListAgentApprovals(r.Context(), user.ID, agent.ID)
	if err != nil {
		return
	}
	var earlier []earlierRow
	for _, row := range rows {
		card := s.presentApproval(row, user, from(r).CSRF)
		if row.Status == "expired" {
			card.Line = "No answer in 10 min, denied"
		}
		if row.Status == "pending" && row.Origin == "web" {
			p.ApprovalsWaiting = append(p.ApprovalsWaiting, card)
			continue
		}
		earlier = append(earlier, earlierRow{
			Tool: card.Tool, Line: card.Line, When: card.When, OK: row.Status == "approved",
		})
	}
	if len(earlier) > 20 {
		earlier = earlier[len(earlier)-20:]
	}
	// Oldest waiting first. Earlier reads newest first.
	for i, j := 0, len(earlier)-1; i < j; i, j = i+1, j-1 {
		earlier[i], earlier[j] = earlier[j], earlier[i]
	}
	p.ApprovalsEarlier = earlier
}

func (s *Server) fillToasts(w http.ResponseWriter, r *http.Request, current string, p *page) {
	rows, err := s.store.ListPendingApprovals(r.Context(), p.User.ID)
	if err != nil {
		return
	}
	seen := toastedSet(r)
	var fresh []string
	for _, row := range rows {
		if row.AgentID == current || seen[row.ID] {
			continue
		}
		tool := row.Tool
		if tool == "" {
			tool = "a tool"
		}
		p.Toasts = append(p.Toasts, toastView{
			AgentName: agentLabel(*p, row.AgentID),
			Tool:      tool,
			Href:      "/agents/" + url.PathEscape(row.AgentID) + "?panel=approvals#appr-" + row.ID,
		})
		fresh = append(fresh, row.ID)
	}
	if len(fresh) > 0 {
		rememberToasts(w, r, fresh)
	}
}

func agentLabel(p page, id string) string {
	if p.Agent.ID == id && p.Agent.Name != "" {
		return p.Agent.Name
	}
	for _, sec := range p.Sections {
		for _, a := range sec.Agents {
			if a.ID == id && a.Name != "" {
				return a.Name
			}
		}
	}
	return "An agent"
}

func toastedSet(r *http.Request) map[string]bool {
	out := map[string]bool{}
	c, err := r.Cookie(toastCookie)
	if err != nil {
		return out
	}
	for _, id := range strings.Split(c.Value, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func rememberToasts(w http.ResponseWriter, r *http.Request, fresh []string) {
	var ids []string
	if c, err := r.Cookie(toastCookie); err == nil {
		for _, id := range strings.Split(c.Value, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
	}
	for _, id := range fresh {
		if id == "" || have[id] {
			continue
		}
		ids = append(ids, id)
		have[id] = true
	}
	if len(ids) > 40 {
		ids = ids[len(ids)-40:]
	}
	http.SetCookie(w, &http.Cookie{
		Name:     toastCookie,
		Value:    strings.Join(ids, ","),
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) approvalsLive(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.User = user
	current := r.URL.Query().Get("agent")
	s.fillToasts(w, r, current, &p)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len(p.Toasts) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = s.render(w, http.StatusOK, "toastList", p)
}

func relAge(stamp string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	m := int(d / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	h := m / 60
	if h < 48 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d d", h/24)
}
