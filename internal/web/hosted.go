package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/scott/castbot/internal/md"
	pagedoc "github.com/scott/castbot/internal/page"
	"github.com/scott/castbot/internal/store"
)

type hostedView struct {
	Title   string
	Missing bool
	Blocks  []hostedBlock
	Notice  string
	Error   string
	Owner   *hostedOwner
}

// hostedOwner is the signed-in owner's controls. Visitors do not get this block.
type hostedOwner struct {
	Token, CSRF, Title, Link, Visibility, Workspace string
	People                                          []hostedPick
	Agents                                          []hostedPick
}

type hostedPick struct {
	ID, Label string
	Checked   bool
}

type hostedBlock struct {
	Kind    string
	Label   string
	Text    string
	Tone    string
	HTML    template.HTML
	SVG     template.HTML
	Caption string
	When    string
	Hidden  bool
	Columns []string
	Items   []hostedStat
	Rows    []hostedRow
	Options []hostedOption
}

type hostedStat struct {
	Label, Value, Note string
}

type hostedRow struct {
	Cells  []string
	When   string
	Hidden bool
}

type hostedOption struct {
	Name, Value string
	Checked     bool
}

func (s *Server) hostedPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	st := from(r)
	// A link page is readable without an account. The owner, when signed in,
	// still gets rename, share, and delete on that same response.
	if row, err := s.store.PageByLink(r.Context(), token); err == nil {
		s.writeHosted(w, r, row, st.User, http.StatusOK, hostedNotice(r), "")
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	need, err := s.store.PageRequiresSignIn(r.Context(), token)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if !need {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return
	}
	if st.User == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	row, err := s.store.PageForUser(r.Context(), st.User.ID, token)
	if errors.Is(err, store.ErrNotFound) {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.writeHosted(w, r, row, st.User, http.StatusOK, hostedNotice(r), "")
}

func (s *Server) hostedRename(w http.ResponseWriter, r *http.Request) {
	user, row, ok := s.ownerPage(w, r)
	if !ok {
		return
	}
	updated, err := s.store.RenamePage(r.Context(), user.ID, "", row.ID, r.FormValue("title"))
	if errors.Is(err, store.ErrInvalid) {
		s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", pageTitle)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/p/"+updated.Token+"?notice=renamed", http.StatusSeeOther)
}

func (s *Server) hostedShare(w http.ResponseWriter, r *http.Request) {
	user, row, ok := s.ownerPage(w, r)
	if !ok {
		return
	}
	vis, visOK := pageVisStore(r.FormValue("visibility"))
	if !visOK {
		s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", "Choose who can view this page.")
		return
	}
	var userIDs, agentIDs []string
	if vis == store.PageInvited {
		var errText string
		var matched bool
		userIDs, errText, matched = s.matchPagePeople(r.Context(), user.ID, r.Form["person"])
		if !matched {
			s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", errText)
			return
		}
		agentIDs, errText, matched = s.matchPageAgents(r.Context(), user.ID, r.Form["agent"])
		if !matched {
			s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", errText)
			return
		}
		if len(userIDs)+len(agentIDs) == 0 {
			s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", pageWho)
			return
		}
	}
	updated, err := s.store.SharePage(r.Context(), user.ID, "", row.ID, vis, userIDs, agentIDs)
	if errors.Is(err, store.ErrInvalid) {
		s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", pageWho)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		if _, look := s.store.PageForUser(r.Context(), user.ID, row.ID); errors.Is(look, store.ErrNotFound) {
			s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
			return
		}
		s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", "Choose people in this workspace.")
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/p/"+updated.Token+"?notice=shared", http.StatusSeeOther)
}

func (s *Server) hostedDelete(w http.ResponseWriter, r *http.Request) {
	user, row, ok := s.ownerPage(w, r)
	if !ok {
		return
	}
	if r.FormValue("confirm") != "yes" {
		s.writeHosted(w, r, row, &user, http.StatusBadRequest, "", "Confirm the delete.")
		return
	}
	if err := s.store.DeletePage(r.Context(), user.ID, "", row.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/p/"+row.Token, http.StatusSeeOther)
}

// ownerPage loads a page and refuses anyone except its owner.
// PageForUser also returns a workspace or link page to other people, so the
// owner check is separate. The response matches a missing page.
func (s *Server) ownerPage(w http.ResponseWriter, r *http.Request) (store.User, store.Page, bool) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return store.User{}, store.Page{}, false
	}
	row, err := s.store.PageForUser(r.Context(), user.ID, r.PathValue("token"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return store.User{}, store.Page{}, false
	}
	if err != nil || row.UserID != user.ID {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return store.User{}, store.Page{}, false
	}
	return user, row, true
}

func (s *Server) writeHosted(w http.ResponseWriter, r *http.Request, row store.Page, user *store.User, status int, notice, formErr string) {
	doc, err := pagedoc.Parse(row.Body)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	v := hostedView{Title: row.Title, Blocks: hostedBlocks(doc)}
	if user != nil && user.ID == row.UserID {
		owner, err := s.hostedOwner(r, *user, row)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		v.Owner = &owner
		v.Notice = notice
		v.Error = formErr
	}
	s.renderHosted(w, status, v)
}

func (s *Server) hostedOwner(r *http.Request, user store.User, row store.Page) (hostedOwner, error) {
	ctx := r.Context()
	members, err := s.store.ListMembers(ctx, user.ID)
	if err != nil {
		return hostedOwner{}, err
	}
	grants, err := s.store.PageGrants(ctx, user.ID, row.ID)
	if err != nil {
		return hostedOwner{}, err
	}
	on := map[string]bool{}
	for _, grant := range grants {
		on[grant.Kind+"/"+grant.GranteeID] = true
	}
	var people []hostedPick
	var agents []hostedPick
	for _, member := range members {
		if member.ID != user.ID {
			people = append(people, hostedPick{
				ID: member.ID, Label: personLabel(member), Checked: on["user/"+member.ID],
			})
		}
		rows, err := s.store.ListAgents(ctx, member.ID)
		if err != nil {
			return hostedOwner{}, err
		}
		for _, agent := range rows {
			agents = append(agents, hostedPick{
				ID: agent.ID, Label: agent.Name, Checked: on["agent/"+agent.ID],
			})
		}
	}
	workspace := strings.TrimSpace(user.WorkspaceName)
	if workspace == "" {
		workspace = "Workspace"
	}
	return hostedOwner{
		Token: row.Token, CSRF: from(r).CSRF, Title: row.Title,
		Link: pageURL(r, row.Token), Visibility: row.Visibility, Workspace: workspace,
		People: people, Agents: agents,
	}, nil
}

func personLabel(user store.User) string {
	name := strings.TrimSpace(user.DisplayName)
	email := strings.TrimSpace(user.Email)
	if name == "" {
		return email
	}
	if email == "" {
		return name
	}
	return name + " · " + email
}

func pageURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/p/" + token
}

func hostedNotice(r *http.Request) string {
	switch r.URL.Query().Get("notice") {
	case "renamed":
		return "Title saved."
	case "shared":
		return "Sharing saved."
	default:
		return ""
	}
}

func (s *Server) renderHosted(w http.ResponseWriter, status int, v hostedView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.WriteHeader(status)
	if err := views.ExecuteTemplate(w, "hosted", v); err != nil {
		log.Printf("render hosted failed")
	}
}

func hostedBlocks(doc pagedoc.Document) []hostedBlock {
	defaults := map[string]string{}
	for _, block := range doc.Blocks {
		if block.Type == "control" && len(block.Options) > 0 {
			defaults[block.ID] = block.Options[0]
		}
	}
	out := make([]hostedBlock, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		switch block.Type {
		case "markdown":
			out = append(out, hostedBlock{
				Kind: "markdown", HTML: template.HTML(md.Render(block.Text)),
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "callout":
			out = append(out, hostedBlock{
				Kind: "callout", Tone: block.Tone, Text: block.Text,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "stats":
			items := make([]hostedStat, len(block.Items))
			for i, item := range block.Items {
				items[i] = hostedStat{Label: item.Label, Value: item.Value, Note: item.Note}
			}
			out = append(out, hostedBlock{
				Kind: "stats", Items: items,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "table":
			rows := make([]hostedRow, len(block.Rows))
			for i, row := range block.Rows {
				rows[i] = hostedRow{Cells: row.Cells, When: whenAttr(row.When), Hidden: hideWhen(row.When, defaults)}
			}
			out = append(out, hostedBlock{
				Kind: "table", Caption: block.Caption, Columns: block.Columns, Rows: rows,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "chart":
			out = append(out, hostedBlock{
				Kind: "chart", Caption: block.Title, SVG: chartSVG(block),
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "control":
			options := make([]hostedOption, len(block.Options))
			for i, option := range block.Options {
				options[i] = hostedOption{Name: block.ID, Value: option, Checked: i == 0}
			}
			out = append(out, hostedBlock{Kind: "control", Label: block.Label, Options: options})
		}
	}
	return out
}

func whenAttr(when map[string]string) string {
	if len(when) == 0 {
		return ""
	}
	raw, err := json.Marshal(when)
	if err != nil {
		return ""
	}
	return string(raw)
}

func hideWhen(when map[string]string, defaults map[string]string) bool {
	for key, want := range when {
		if defaults[key] != want {
			return true
		}
	}
	return false
}

func chartSVG(block pagedoc.Block) template.HTML {
	const (
		width  = 640
		height = 240
		left   = 44
		right  = 12
		top    = 16
		bottom = 36
	)
	cats := chartCats(block.Series)
	if len(cats) == 0 {
		return ""
	}
	minY, maxY := 0.0, 0.0
	seen := false
	for _, series := range block.Series {
		for _, pt := range series.Points {
			if !seen || pt.Y < minY {
				minY = pt.Y
			}
			if !seen || pt.Y > maxY {
				maxY = pt.Y
			}
			seen = true
		}
	}
	if minY > 0 {
		minY = 0
	}
	if maxY < 0 {
		maxY = 0
	}
	if maxY == minY {
		maxY = minY + 1
	}
	plotW := float64(width - left - right)
	plotH := float64(height - top - bottom)
	yAt := func(y float64) float64 {
		return float64(top) + (maxY-y)/(maxY-minY)*plotH
	}
	base := yAt(0)
	label := block.Title
	if label == "" {
		label = "Chart"
	}
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 640 240" role="img" aria-label="`)
	b.WriteString(template.HTMLEscapeString(label))
	b.WriteString(`">`)
	fills := []string{"var(--acc)", "var(--start)", "var(--ok)", "var(--warn)"}
	slot := plotW / float64(len(cats))
	if block.Kind == "bar" {
		inner := slot / float64(len(block.Series)+1)
		for si, series := range block.Series {
			vals := map[string]float64{}
			for _, pt := range series.Points {
				vals[pt.X] = pt.Y
			}
			fill := fills[si%len(fills)]
			for ci, cat := range cats {
				y, ok := vals[cat]
				if !ok {
					continue
				}
				y1 := yAt(y)
				y0 := base
				if y1 > y0 {
					y0, y1 = y1, y0
				}
				x := float64(left) + float64(ci)*slot + inner*float64(si+1)
				h := y0 - y1
				if h < 1 {
					h = 1
				}
				b.WriteString(`<rect x="`)
				b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
				b.WriteString(`" y="`)
				b.WriteString(strconv.FormatFloat(y1, 'f', 1, 64))
				b.WriteString(`" width="`)
				b.WriteString(strconv.FormatFloat(inner*0.8, 'f', 1, 64))
				b.WriteString(`" height="`)
				b.WriteString(strconv.FormatFloat(h, 'f', 1, 64))
				b.WriteString(`" fill="`)
				b.WriteString(fill)
				b.WriteString(`"></rect>`)
			}
		}
	} else {
		for si, series := range block.Series {
			vals := map[string]float64{}
			for _, pt := range series.Points {
				vals[pt.X] = pt.Y
			}
			b.WriteString(`<polyline fill="none" stroke="`)
			b.WriteString(fills[si%len(fills)])
			b.WriteString(`" stroke-width="2" points="`)
			first := true
			for ci, cat := range cats {
				y, ok := vals[cat]
				if !ok {
					continue
				}
				if !first {
					b.WriteByte(' ')
				}
				first = false
				x := float64(left) + float64(ci)*slot + slot/2
				b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
				b.WriteByte(',')
				b.WriteString(strconv.FormatFloat(yAt(y), 'f', 1, 64))
			}
			b.WriteString(`"></polyline>`)
		}
	}
	for ci, cat := range cats {
		x := float64(left) + float64(ci)*slot + slot/2
		b.WriteString(`<text x="`)
		b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		b.WriteString(`" y="`)
		b.WriteString(strconv.Itoa(height - 12))
		b.WriteString(`" text-anchor="middle" fill="currentColor" font-size="12">`)
		b.WriteString(template.HTMLEscapeString(chartLabel(cat)))
		b.WriteString(`</text>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func chartCats(series []pagedoc.Series) []string {
	var cats []string
	seen := map[string]bool{}
	for _, series := range series {
		for _, pt := range series.Points {
			if seen[pt.X] {
				continue
			}
			seen[pt.X] = true
			cats = append(cats, pt.X)
		}
	}
	return cats
}

func chartLabel(s string) string {
	if utf8.RuneCountInString(s) <= 12 {
		return s
	}
	n := 0
	for i := range s {
		if n == 12 {
			return s[:i]
		}
		n++
	}
	return s
}
