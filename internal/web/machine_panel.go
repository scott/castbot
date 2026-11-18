package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
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
}

func (s *Server) machineHistory(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	shown, more, next := pageHistory(s.replaceHistory(r.Context(), user, agent.ID, userLocation(user.Timezone)), offset)
	// beforeend inserts the children of the swapped element. The rows are
	// wrapped in a list so each one lands in the history as its own item.
	s.render(w, http.StatusOK, "machineHistoryMore", page{
		Agent:   agentView{ID: agent.ID},
		Replace: replaceView{History: shown, HistoryMore: more, HistoryNext: next},
	})
}
