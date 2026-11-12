package web

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
)

type filesPanel struct {
	View           string
	Paused         bool
	Side           string
	NoteLead       string
	NoteRest       string
	Wake           bool
	WorkspaceCount int
	UploadCount    int
	Groups         []fileGroup
	Uploads        []fileRow
	Held           []fileRow
	Detail         *fileDetail
	Empty          bool
	QuotaWarn      bool
	Foot           string
	UploadHref     string
	WorkspaceHref  string
	UploadsHref    string
	BackHref       string
}

type fileGroup struct {
	Label string
	Rows  []fileRow
}

type fileRow struct {
	Name     string
	Meta     string
	Href     string
	Download string
	Wake     bool
}

type fileDetail struct {
	Name        string
	Path        string
	Size        string
	From        string
	Seen        string
	Type        string
	Download    string
	Mention     string
	Lines       []previewLine
	LineNote    string
	ShowPreview bool
}

type previewLine struct {
	N    int
	Text string
}

type fileHitView struct {
	Name, Sub, Href, Letter, Color string
}

func (s *Server) loadFiles(r *http.Request, user store.User, agent store.Agent) filesPanel {
	loc := userLocation(user.Timezone)
	now := s.store.Now()
	view := r.URL.Query().Get("view")
	if view != "uploads" {
		view = "workspace"
	}
	base := "/agents/" + url.PathEscape(agent.ID)
	vm, vmErr := s.store.LiveVM(r.Context(), user.ID, agent.ID)
	paused := vmErr == nil && vm.State == "paused"
	running := vmErr == nil && vm.State == "running" && !s.replacing(r.Context(), user.ID, agent.ID)
	if running {
		s.refreshWorkspace(r, user.ID, agent.ID)
	}
	cached, _ := s.store.ListCached(r.Context(), user.ID, agent.ID)
	uploads, _ := s.store.ListUploads(r.Context(), user.ID, agent.ID)
	var workspace []store.CachedFile
	for _, row := range cached {
		if strings.HasPrefix(row.Name, "uploads/") {
			continue
		}
		workspace = append(workspace, row)
	}
	panel := filesPanel{
		View: view, Paused: paused,
		Side:          agent.Name + " only",
		WorkspaceHref: base + "?panel=files",
		UploadsHref:   base + "?panel=files&view=uploads",
		BackHref:      base + "?panel=files",
	}
	if view == "uploads" {
		panel.BackHref = base + "?panel=files&view=uploads"
	}
	if paused {
		panel.Side += " · paused"
		panel.Wake = true
		full := pausedNote(agent.Name, latestSeen(workspace, uploads, loc))
		panel.NoteLead = agent.Name + " is paused."
		panel.NoteRest = strings.TrimPrefix(full, panel.NoteLead)
	}
	panel.WorkspaceCount = len(workspace)
	var listed []store.File
	for _, row := range uploads {
		if row.Status == "held" {
			panel.Held = append(panel.Held, fileRow{
				Name: row.Name,
				Meta: formatSize(row.Size),
				Href: base + "?panel=files&file=" + url.QueryEscape(row.ID) + "&view=uploads",
			})
			continue
		}
		listed = append(listed, row)
	}
	panel.UploadCount = len(listed)
	panel.Groups = groupWorkspace(base, workspace, paused, loc, now)
	for _, row := range listed {
		panel.Uploads = append(panel.Uploads, fileRow{
			Name:     row.Name,
			Meta:     "uploads · " + formatSize(row.Size) + " · " + uploadedLabel(row.CreatedAt, now, loc),
			Href:     base + "?panel=files&file=" + url.QueryEscape(row.ID) + "&view=uploads",
			Download: base + "/files/" + url.PathEscape(row.ID) + "/download",
		})
	}
	if usage, err := s.store.UploadUsage(r.Context(), user.ID, agent.WorkspaceID); err == nil && usage*10 >= uploadQuota*8 && usage < uploadQuota {
		panel.QuotaWarn = true
	}
	if view == "uploads" {
		panel.Foot = "Uploads are saved in castbot and copied into " + agent.Name + "'s machine at workspace/uploads. " + agent.Name + " is told the path in your next message."
		panel.Empty = len(panel.Uploads) == 0 && len(panel.Held) == 0
	} else {
		panel.Foot = "Workspace is the folder " + agent.Name + " works in on its machine (/app/agent/workspace). Downloads stream through castbot; nothing is kept in your browser."
		panel.Empty = len(workspace) == 0
	}
	if id := r.URL.Query().Get("file"); id != "" {
		if detail, ok := s.fileDetail(r, user, agent, id, loc, now); ok {
			panel.Detail = &detail
			panel.Empty = false
		}
	}
	return panel
}

func (s *Server) refreshWorkspace(r *http.Request, userID, agentID string) {
	h, _, _ := s.hostConfig()
	if h == nil {
		return
	}
	endpoint, token, err := s.store.VMCredentials(r.Context(), userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		return
	}
	listed, err := h.ListWorkspace(r.Context(), endpoint, token)
	if err != nil {
		return
	}
	rows := make([]store.ListedFile, 0, len(listed))
	for _, item := range listed {
		rows = append(rows, store.ListedFile{Name: item.Name, Size: item.Size})
	}
	_ = s.store.ObserveWorkspace(r.Context(), userID, agentID, rows)
}

func (s *Server) fileDetail(r *http.Request, user store.User, agent store.Agent, id string, loc *time.Location, now time.Time) (fileDetail, bool) {
	base := "/agents/" + url.PathEscape(agent.ID)
	if row, err := s.store.GetFile(r.Context(), user.ID, id); err == nil && row.AgentID == agent.ID {
		d := fileDetail{
			Name: row.Name, Path: row.Path, Size: formatSize(row.Size),
			From: "You (upload)", Seen: detailSeen(row.CreatedAt, now, loc),
			Type: fileTypeLabel(row.Name), Mention: row.Path,
			Download: base + "/files/" + url.PathEscape(row.ID) + "/download",
		}
		if previewKind(row.Name) && row.Size <= previewCap {
			if blob, ok := s.blobPath(row.SHA256); ok {
				if f, err := os.Open(blob); err == nil {
					d.Lines, d.LineNote, d.ShowPreview = readPreview(f)
					f.Close()
				}
			}
		}
		return d, true
	}
	cached, err := s.store.GetCached(r.Context(), user.ID, id)
	if err != nil || cached.AgentID != agent.ID {
		return fileDetail{}, false
	}
	d := fileDetail{
		Name: path.Base(cached.Name), Path: "workspace/" + cached.Name, Size: formatSize(cached.Size),
		From: agent.Name + " (workspace)", Seen: detailSeen(cached.SeenAt, now, loc),
		Type: fileTypeLabel(cached.Name), Mention: "workspace/" + cached.Name,
		Download: base + "/files/" + url.PathEscape(cached.ID) + "/download",
	}
	if previewKind(cached.Name) && cached.Size <= previewCap && !s.workspacePaused(r, user.ID, agent.ID) && !s.replacing(r.Context(), user.ID, agent.ID) {
		if rc, err := s.openWorkspace(r, user.ID, agent.ID, cached.Name); err == nil {
			d.Lines, d.LineNote, d.ShowPreview = readPreview(rc)
			rc.Close()
		}
	}
	return d, true
}

func readPreview(r io.Reader) ([]previewLine, string, bool) {
	buf, err := io.ReadAll(io.LimitReader(r, previewCap+1))
	if err != nil || int64(len(buf)) > previewCap || !textBytes(buf) {
		return nil, "", false
	}
	raw := strings.ReplaceAll(string(buf), "\r\n", "\n")
	parts := strings.Split(raw, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	total := len(parts)
	if total > 400 {
		parts = parts[:400]
	}
	lines := make([]previewLine, 0, len(parts))
	for i, line := range parts {
		lines = append(lines, previewLine{N: i + 1, Text: line})
	}
	note := "first " + itoa(len(lines)) + " of " + itoa(total) + " lines"
	return lines, note, true
}

func textBytes(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return false
		}
	}
	return true
}

func groupWorkspace(base string, rows []store.CachedFile, paused bool, loc *time.Location, now time.Time) []fileGroup {
	by := map[string][]fileRow{}
	var keys []string
	for _, row := range rows {
		dir := path.Dir(row.Name)
		label := ""
		place := "workspace"
		if dir != "." && dir != "" {
			label = dir + "/"
			place = "workspace/" + dir
		}
		item := fileRow{
			Name:     path.Base(row.Name),
			Meta:     place + " · " + formatSize(row.Size) + " · seen " + seenLabel(row.SeenAt, now, loc),
			Href:     base + "?panel=files&file=" + url.QueryEscape(row.ID),
			Download: base + "/files/" + url.PathEscape(row.ID) + "/download",
			Wake:     paused,
		}
		if _, ok := by[label]; !ok {
			keys = append(keys, label)
		}
		by[label] = append(by[label], item)
	}
	sort.Strings(keys)
	var out []fileGroup
	for _, key := range keys {
		out = append(out, fileGroup{Label: key, Rows: by[key]})
	}
	return out
}

func (s *Server) attachSearch(r *http.Request, p *page) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || p.User.ID == "" {
		return
	}
	p.SearchQuery = q
	low := strings.ToLower(q)
	for _, sec := range p.Sections {
		for _, a := range sec.Agents {
			if strings.Contains(strings.ToLower(a.Name), low) || strings.Contains(strings.ToLower(a.Title), low) {
				p.SearchAgents = append(p.SearchAgents, a)
			}
		}
	}
	hits, err := s.store.SearchFiles(r.Context(), p.User.ID, q)
	if err != nil {
		return
	}
	for _, hit := range hits {
		agent, err := s.store.GetAgent(r.Context(), p.User.ID, hit.AgentID)
		if err != nil {
			continue
		}
		place := "workspace"
		if hit.Source == "upload" {
			place = "uploads"
		} else if dir := path.Dir(hit.Name); dir != "." && dir != "" {
			place = "workspace/" + dir
		}
		p.FileHits = append(p.FileHits, fileHitView{
			Name:   path.Base(hit.Name),
			Sub:    hit.AgentName + " · " + place + " · " + formatSize(hit.Size),
			Href:   "/agents/" + url.PathEscape(hit.AgentID) + "?panel=files&file=" + url.QueryEscape(hit.ID),
			Letter: letter(agent.Name), Color: agent.Color,
		})
	}
}

func (s *Server) pendingPathText(r *http.Request, userID, agentID, agentName string) (string, []string) {
	rows, err := s.store.PendingAnnouncements(r.Context(), userID, agentID)
	if err != nil || len(rows) == 0 {
		return "", nil
	}
	var lines, ids []string
	for _, row := range rows {
		lines = append(lines, pathLine(agentName, row.Name))
		ids = append(ids, row.ID)
	}
	return strings.Join(lines, "\n"), ids
}

func splitUserBody(body string) (text string, lines []string, names []string) {
	for _, line := range strings.Split(body, "\n") {
		if name, ok := uploadPathName(line); ok {
			lines = append(lines, strings.TrimSpace(line))
			names = append(names, name)
			continue
		}
		text += line + "\n"
	}
	text = strings.TrimSpace(text)
	return text, lines, names
}

func uploadPathName(line string) (string, bool) {
	const mid = " got the file at workspace/uploads/"
	const suf = ", named in your message."
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, suf) {
		return "", false
	}
	i := strings.Index(line, mid)
	if i <= 0 {
		return "", false
	}
	name := strings.TrimSuffix(line[i+len(mid):], suf)
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", false
	}
	return name, true
}

func pausedNote(name, when string) string {
	tail := " Opening Files does not wake it. Downloading a workspace file or tapping Wake does."
	if when == "" {
		return name + " is paused. This list is from the last time it ran." + tail
	}
	return name + " is paused. This list is from " + when + ", when it last ran." + tail
}

func latestSeen(cached []store.CachedFile, uploads []store.File, loc *time.Location) string {
	var latest time.Time
	for _, row := range cached {
		if row.SeenAt.After(latest) {
			latest = row.SeenAt
		}
	}
	for _, row := range uploads {
		if row.CreatedAt.After(latest) {
			latest = row.CreatedAt
		}
	}
	if latest.IsZero() {
		return ""
	}
	if loc != nil {
		latest = latest.In(loc)
	}
	return latest.Format("3:04 PM")
}

func seenLabel(t, now time.Time, loc *time.Location) string {
	if loc != nil {
		t = t.In(loc)
		now = now.In(loc)
	}
	if t.IsZero() {
		return "just now"
	}
	if sameDay(t, now) {
		return t.Format("3:04 PM")
	}
	if now.Sub(t) < 7*24*time.Hour && now.After(t) {
		return t.Format("Mon")
	}
	return t.Format("Jan 2")
}

func detailSeen(t, now time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "just now"
	}
	if loc != nil {
		t = t.In(loc)
		now = now.In(loc)
	}
	if sameDay(t, now) {
		return "Today " + t.Format("3:04 PM")
	}
	return t.Format("Jan 2, 3:04 PM")
}

func uploadedLabel(t, now time.Time, loc *time.Location) string {
	if loc != nil {
		t = t.In(loc)
		now = now.In(loc)
	}
	if t.IsZero() {
		return "uploaded by you"
	}
	if sameDay(t, now) {
		return "uploaded by you " + t.Format("3:04 PM")
	}
	if now.Sub(t) < 7*24*time.Hour && now.After(t) {
		return "uploaded by you " + t.Format("Mon")
	}
	return "uploaded by you " + t.Format("Jan 2")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func formatSize(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return itoa(int(n)) + " B"
	}
	if n < 1024*1024 {
		return itoa(int((n+512)/1024)) + " KB"
	}
	return formatMB(n) + " MB"
}

func fileTypeLabel(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md":
		return "Text, Markdown"
	case ".txt", ".log":
		return "Text"
	case ".csv":
		return "CSV"
	case ".json":
		return "JSON"
	case ".html", ".htm":
		return "HTML"
	case ".pdf":
		return "PDF"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "Image"
	default:
		return "File"
	}
}
