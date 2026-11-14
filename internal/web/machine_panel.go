package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
)

const (
	guestReadLimit = 2 * time.Second
	logLineCap     = 80
	logLineRunes   = 500
)

// Microdollars per hour while running, and per day while paused.
// One dollar is 1,000,000. Display rounds half up to cents.
func hourlyMicro(size string) int64 {
	switch size {
	case "2:4096":
		return 78000
	case "8:16384":
		return 312000
	default:
		return 39000
	}
}

func dailyDiskMicro(size string) int64 {
	switch size {
	case "2:4096":
		return 20000
	case "8:16384":
		return 80000
	default:
		return 10000
	}
}

func spanMicro(span store.UsageSpan) int64 {
	switch span.Kind {
	case "compute":
		return span.Seconds * hourlyMicro(span.Size) / 3600
	case "disk":
		return span.Seconds * dailyDiskMicro(span.Size) / 86400
	default:
		return 0
	}
}

func formatUSD(micro int64) string {
	if micro < 0 {
		micro = 0
	}
	cents := (micro + 5000) / 10000
	dollars := cents / 100
	rest := cents % 100
	return "$" + itoa(int(dollars)) + "." + twoDigits(int(rest))
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func monthStart(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = userLocation("")
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
}

func (s *Server) monthEstimate(ctx context.Context, user store.User, agentID string) string {
	spans, err := s.store.UsageIn(ctx, user.ID, agentID, monthStart(s.store.Now(), userLocation(user.Timezone)), s.store.Now())
	if err != nil {
		return formatUSD(0)
	}
	var total int64
	for _, span := range spans {
		total += spanMicro(span)
	}
	return formatUSD(total)
}

func (s *Server) fillAccountUsage(ctx context.Context, user store.User, p *page) {
	p.Usage = formatUSD(0)
	spans, err := s.store.UsageIn(ctx, user.ID, "", monthStart(s.store.Now(), userLocation(user.Timezone)), s.store.Now())
	if err != nil {
		return
	}
	by := map[string]int64{}
	var total int64
	for _, span := range spans {
		n := spanMicro(span)
		by[span.AgentID] += n
		total += n
	}
	p.Usage = formatUSD(total)
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return
	}
	for _, agent := range agents {
		n := by[agent.ID]
		_, liveErr := s.store.LiveVM(ctx, user.ID, agent.ID)
		if n == 0 && liveErr != nil {
			continue
		}
		p.UsageLines = append(p.UsageLines, usageLine{
			Name:   agent.Name,
			Amount: formatUSD(n),
			Href:   "/agents/" + url.PathEscape(agent.ID) + "?panel=machine",
		})
	}
}

func machinePanelShown(r *http.Request, p page) bool {
	if p.Panel == "machine" || p.Tab == "machine" {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/machine/refresh")
}

func (s *Server) fillMachineExtras(r *http.Request, user store.User, agent store.Agent, view *machineView) {
	view.Estimate = s.monthEstimate(r.Context(), user, agent.ID)
	if s.replacing(r.Context(), user.ID, agent.ID) {
		view.LogText = ""
		view.LogLive = false
		view.LogNote = "Logs and files wait until the replace is done."
		view.FilesNote = view.LogNote
		return
	}
	s.fillMachineLogs(r.Context(), user.ID, agent.ID, view)
	s.fillMachineFiles(r, user, agent, view)
}

func (s *Server) fillMachineLogs(ctx context.Context, userID, agentID string, view *machineView) {
	view.LogLive = false
	view.LogText = ""
	switch view.State {
	case "running":
		text, note, live := s.readLogTail(ctx, userID, agentID)
		view.LogText = text
		view.LogNote = note
		view.LogLive = live
	case "paused":
		view.LogNote = "Logs stay on the machine. Wake it to read the tail. This check does not wake it."
	default:
		view.LogNote = "No log tail yet."
	}
}

func (s *Server) readLogTail(ctx context.Context, userID, agentID string) (text, note string, live bool) {
	h, _, _ := s.hostConfig()
	if h == nil {
		return "", "No log tail yet.", false
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		return "", "No log tail yet.", false
	}
	cctx, cancel := context.WithTimeout(ctx, guestReadLimit)
	raw, err := h.Logs(cctx, endpoint, token)
	cancel()
	if err != nil {
		return "", "The log tail could not be read. The machine was not woken.", true
	}
	secrets := []string{token}
	if granted, gerr := s.store.GrantedSecrets(ctx, userID, agentID); gerr == nil {
		for _, sec := range granted {
			if len(sec.Value) >= 8 {
				secrets = append(secrets, sec.Value)
			}
		}
	}
	text = scrubLog(raw, secrets)
	if strings.TrimSpace(text) == "" {
		return "", "No log lines yet.", true
	}
	return text, "Latest lines from the machine. A secret is shown as [redacted].", true
}

func scrubLog(raw string, secrets []string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var cleaned strings.Builder
	for _, r := range raw {
		if r == '\n' || r == '\t' || r >= 0x20 {
			cleaned.WriteRune(r)
		}
	}
	lines := strings.Split(cleaned.String(), "\n")
	if len(lines) > logLineCap {
		lines = lines[len(lines)-logLineCap:]
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, sec := range secrets {
			if len(sec) >= 8 && sec != "" {
				line = strings.ReplaceAll(line, sec, "[redacted]")
			}
		}
		if len(line) > logLineRunes {
			line = line[:logLineRunes]
		}
		line, _ = redactSecrets(line)
		out = append(out, line)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func (s *Server) fillMachineFiles(r *http.Request, user store.User, agent store.Agent, view *machineView) {
	if view.State == "" {
		view.FilesNote = "No machine yet, so there are no workspace files."
		return
	}
	listErr := false
	if view.State == "running" {
		if err := s.refreshWorkspaceLimited(r.Context(), user.ID, agent.ID); err != nil {
			listErr = true
		}
	}
	cached, err := s.store.ListCached(r.Context(), user.ID, agent.ID)
	if err != nil {
		view.FilesNote = "No workspace files yet."
		return
	}
	base := "/agents/" + url.PathEscape(agent.ID)
	for _, row := range cached {
		if strings.HasPrefix(row.Name, "uploads/") {
			continue
		}
		view.Files = append(view.Files, fileRow{
			Name:     row.Name,
			Meta:     formatSize(row.Size),
			Href:     base + "?panel=files&file=" + url.QueryEscape(row.ID),
			Download: base + "/files/" + url.PathEscape(row.ID) + "/download",
		})
	}
	if listErr {
		view.FilesNote = "The file list could not be read. The machine was not woken."
		return
	}
	if view.State == "paused" {
		if len(view.Files) == 0 {
			view.FilesNote = "No workspace files yet. Opening this panel does not wake the machine."
		} else {
			view.FilesNote = "Last list from the machine. Opening this panel does not wake it."
		}
		return
	}
	if view.State != "running" {
		if len(view.Files) == 0 {
			view.FilesNote = "No workspace files yet."
		} else {
			view.FilesNote = "Last list from the machine. Opening this panel does not wake it."
		}
		return
	}
	if len(view.Files) == 0 {
		view.FilesNote = "No workspace files yet."
		return
	}
	view.FilesNote = "Workspace files on this machine. Downloads stream through castbot."
}

func (s *Server) refreshWorkspaceLimited(ctx context.Context, userID, agentID string) error {
	h, _, _ := s.hostConfig()
	if h == nil {
		return nil
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, guestReadLimit)
	listed, err := h.ListWorkspace(cctx, endpoint, token)
	cancel()
	if err != nil {
		return err
	}
	rows := make([]store.ListedFile, 0, len(listed))
	for _, item := range listed {
		rows = append(rows, store.ListedFile{Name: item.Name, Size: item.Size})
	}
	return s.store.ObserveWorkspace(ctx, userID, agentID, rows)
}

func (s *Server) machineLogs(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	view := s.machineFromStore(r.Context(), user.ID, &agent, false)
	if s.replacing(r.Context(), user.ID, agent.ID) {
		view.LogText = ""
		view.LogLive = false
		view.LogNote = "Logs and files wait until the replace is done."
	} else {
		s.fillMachineLogs(r.Context(), user.ID, agent.ID, &view)
	}
	s.render(w, http.StatusOK, "machineLogs", page{
		Agent:   agentView{ID: agent.ID},
		Machine: view,
	})
}
