package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"html/template"
	"strings"
	"sync"
	"time"

	"github.com/scott/castbot/internal/md"
	"github.com/scott/castbot/internal/store"
)

func fillMessage(v *msgView) {
	switch v.Author {
	case "user":
		if v.Prompt {
			return
		}
		v.HTML = template.HTML(md.LinkPlain(v.Text))
	case "agent":
		v.Copy = base64.StdEncoding.EncodeToString([]byte(v.Body))
		if strings.TrimSpace(v.Body) == "" {
			return
		}
		v.HTML = template.HTML(md.Render(v.Body))
	}
}

type toolFact struct {
	Kind  string
	Label string
	Open  bool
}

func parseTool(payload string) toolFact {
	var p struct {
		Kind   string `json:"kind"`
		Tool   string `json:"tool"`
		Name   string `json:"name"`
		Label  string `json:"label"`
		Title  string `json:"title"`
		Status string `json:"status"`
		Open   *bool  `json:"open"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil {
		return toolFact{}
	}
	kind := firstText(p.Kind, p.Tool, p.Name)
	label := firstText(p.Label, p.Title, kind)
	open := true
	if p.Open != nil {
		open = *p.Open
	}
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "done", "ok", "finished", "closed", "complete":
		open = false
	case "run", "running", "open", "start", "started":
		open = true
	}
	return toolFact{Kind: kind, Label: label, Open: open}
}

func firstText(parts ...string) string {
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			return part
		}
	}
	return ""
}

func (s *Server) machinePhase(ctx context.Context, userID, agentID string) string {
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil {
		return ""
	}
	switch vm.State {
	case "paused":
		return "paused"
	case "creating", "starting", "provisioning":
		return "starting"
	default:
		return ""
	}
}

func (s *Server) activityFor(ctx context.Context, userID string, row store.Run, text string, tool toolFact, pending bool) activityOut {
	in := activityIn{
		Status:          row.Status,
		HasText:         strings.TrimSpace(text) != "",
		PendingApproval: pending || row.Status == "waiting_approval",
		Machine:         s.machinePhase(ctx, userID, row.AgentID),
	}
	if tool.Open && tool.Kind != "" {
		in.ToolKind = tool.Kind
	}
	out := decideActivity(in)
	if out.HideThread {
		return out
	}
	shown := dwellLabel(row.ID, out.Label, s.store.Now())
	if shown != out.Label {
		out.Label = shown
		out.Mode = modeFor(shown)
		out.Hint = ""
	}
	return out
}

func modeFor(label string) string {
	switch label {
	case "Waiting for approval":
		return "held"
	case "Reconnecting":
		return "slow"
	default:
		return ""
	}
}

var dwells = struct {
	mu sync.Mutex
	m  map[string]*labelDwell
}{m: map[string]*labelDwell{}}

func dwellLabel(runID, label string, now time.Time) string {
	dwells.mu.Lock()
	defer dwells.mu.Unlock()
	d := dwells.m[runID]
	if d == nil {
		d = &labelDwell{}
		dwells.m[runID] = d
	}
	return d.next(label, now)
}

func clearDwell(runID string) {
	dwells.mu.Lock()
	delete(dwells.m, runID)
	dwells.mu.Unlock()
}

func (s *Server) paintLive(ctx context.Context, user store.User, row store.Run, agent agentView, csrf string) liveView {
	events, err := s.store.ListRunEvents(ctx, user.ID, row.ID, 0)
	if err != nil {
		events = nil
	}
	text, last, tools, fact, errText := foldRun(events)
	if errText == "" {
		errText = row.Error
	}
	closed, tail := md.Split(text)
	var blocks strings.Builder
	for _, block := range closed {
		blocks.WriteString(md.Render(block.Raw))
	}
	pending := row.Status == "waiting_approval"
	out := s.activityFor(ctx, user.ID, row, text, fact, pending)
	elapsed := ""
	if started, perr := time.Parse(time.RFC3339, row.StartedAt); perr == nil {
		elapsed, _ = formatElapsed(s.store.Now().Sub(started))
	}
	inner := ""
	if strings.TrimSpace(tail.Raw) != "" {
		inner = md.RenderTail(tail)
	}
	if out.HideThread {
		if inner == "" && strings.TrimSpace(text) != "" {
			inner = `<span class="caret" aria-hidden="true"></span>`
		}
	} else {
		inner = strings.ReplaceAll(inner, `<span class="caret" aria-hidden="true"></span>`, "")
	}
	calm := calmFor(agent.Name, errText, strings.TrimSpace(text) != "")
	return liveView{
		RunID: row.ID, Color: agent.Color, Letter: agent.Letter, CSRF: csrf,
		AgentID: agent.ID, AgentName: agent.Name, After: last,
		Text: text, Error: errText,
		HTML:      template.HTML(md.Render(text)),
		Blocks:    template.HTML(blocks.String()),
		Tail:      template.HTML(tailWrap(row.ID, inner)),
		Act:       template.HTML(actSlotHTML(row.ID, actRowHTML(out, elapsed), out.HideThread)),
		Tools:     template.HTML(tools),
		CalmTitle: calm.Title, CalmLine: calm.Line, CalmRetry: calm.Retry, CalmSecond: calm.Second,
		CalmWake: calm.Wake,
	}
}

func foldRun(events []store.RunEvent) (text string, last int, tools string, fact toolFact, errText string) {
	var b strings.Builder
	var rows []string
	for _, ev := range events {
		if ev.Seq > last {
			last = ev.Seq
		}
		switch ev.Kind {
		case "text":
			var payload struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(ev.Payload), &payload) == nil {
				b.WriteString(payload.Text)
			}
		case "tool":
			got := parseTool(ev.Payload)
			if got.Kind == "" {
				continue
			}
			fact = got
			rows = append(rows, toolRowHTML(got))
		case "error":
			var payload struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Payload), &payload) == nil && payload.Message != "" {
				errText = payload.Message
			}
		}
	}
	return b.String(), last, strings.Join(rows, ""), fact, errText
}

func textBefore(events []store.RunEvent, seq int) (before, through string) {
	var b, t strings.Builder
	for _, ev := range events {
		if ev.Kind != "text" {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ev.Payload), &payload) != nil {
			continue
		}
		if ev.Seq < seq {
			b.WriteString(payload.Text)
		}
		if ev.Seq <= seq {
			t.WriteString(payload.Text)
		}
	}
	return b.String(), t.String()
}

func toolRowHTML(fact toolFact) string {
	state := "Done"
	cls := "ok"
	if fact.Open {
		state = "Running"
		cls = "run"
	}
	kind := fact.Kind
	if kind == "" {
		kind = "tool"
	}
	return `<div class="tool"><span class="tn">` + html.EscapeString(kind) + `</span><span class="ta">` + html.EscapeString(fact.Label) + `</span><span class="ts ` + cls + `">` + state + `</span></div>`
}

func (s *Server) decorateAgent(ctx context.Context, userID string, v *agentView, row store.OpenRun, has bool) {
	if v.SubClass == "err" {
		return
	}
	if v.Approvals > 0 {
		v.Sub = joinDot(v.Title, "needs you")
		v.SubClass = ""
		v.ActSub = ""
		return
	}
	if !has || s.isGreetRun(row.ID) {
		return
	}
	fact := parseTool(row.ToolPayload)
	seen := ""
	if row.HasText {
		seen = "x"
	}
	out := s.activityFor(ctx, userID, row.Run, seen, fact, row.Status == "waiting_approval")
	v.ActSub = sidebarActHTML(out)
	v.Sub = out.Label
	v.SubClass = "act-sub"
	if out.Label == "Waking up" {
		v.ChipClass = "start"
		v.ChipLabel = "Waking"
		v.Dot = "start"
	}
}

func sideSpan(v agentView, oob bool) string {
	var b strings.Builder
	b.WriteString(`<span id="ag-sub-`)
	b.WriteString(html.EscapeString(v.ID))
	b.WriteString(`" class="sub`)
	if v.SubClass != "" {
		b.WriteString(" ")
		b.WriteString(html.EscapeString(v.SubClass))
	}
	b.WriteString(`"`)
	if oob {
		b.WriteString(` hx-swap-oob="outerHTML"`)
	}
	b.WriteString(`>`)
	if v.ActSub != "" {
		b.WriteString(string(v.ActSub))
	} else {
		b.WriteString(html.EscapeString(v.Sub))
	}
	b.WriteString(`</span>`)
	return b.String()
}

func (s *Server) sideOOB(ctx context.Context, user store.User, agentID string) string {
	agent, err := s.store.GetAgent(ctx, user.ID, agentID)
	if err != nil {
		return ""
	}
	v := s.paintAgent(ctx, user.ID, agent, false, userLocation(user.Timezone))
	var row store.OpenRun
	has := false
	if rows, err := s.store.ListOpenRuns(ctx, user.ID); err == nil {
		for _, item := range rows {
			if item.AgentID == agentID {
				row = item
				has = true
			}
		}
	}
	s.decorateAgent(ctx, user.ID, &v, row, has)
	return sideSpan(v, true)
}
