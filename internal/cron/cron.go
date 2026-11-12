// Package cron parses 5-field schedules and speaks them in plain language.
// The control plane is the only parser. Pages do not interpret a cron string.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MinInterval is the closest two runs of one routine may be.
	MinInterval = 15 * time.Minute
	// TooClose is the copy shown when a schedule fires more often than MinInterval.
	TooClose = "Runs closer than 15 min apart keep the machine awake all day."
	// BadSchedule is the copy shown when the 5 fields do not parse.
	BadSchedule = "Not a valid schedule. Use 5 parts: minute hour day month weekday, like 0 8 * * 1-5"
	// NoPreview is the copy shown until the schedule parses.
	NoPreview = "No preview until the schedule is valid"
)

// Schedule is one 5-field cron. Star fields match everything.
// When both the day of the month and the weekday are restricted, a day matches
// if either field matches. That is the usual 5-field rule.
type Schedule struct {
	Minutes []int
	Hours   []int
	DOM     []int
	Months  []int
	DOW     []int
	DOMStar bool
	DOWStar bool
	Raw     string
}

// Parse reads minute hour day month weekday.
func Parse(expr string) (Schedule, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("want 5 fields")
	}
	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return Schedule{}, err
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return Schedule{}, err
	}
	dom, domStar, err := parseStarField(fields[2], 1, 31)
	if err != nil {
		return Schedule{}, err
	}
	month, _, err := parseStarField(fields[3], 1, 12)
	if err != nil {
		return Schedule{}, err
	}
	dow, dowStar, err := parseStarField(fields[4], 0, 7)
	if err != nil {
		return Schedule{}, err
	}
	for i, d := range dow {
		if d == 7 {
			dow[i] = 0
		}
	}
	dow = uniqueSorted(dow)
	return Schedule{
		Minutes: minute,
		Hours:   hour,
		DOM:     dom,
		Months:  month,
		DOW:     dow,
		DOMStar: domStar,
		DOWStar: dowStar,
		Raw:     strings.Join(fields, " "),
	}, nil
}

// TooFrequent reports whether two fires can land closer than MinInterval.
func TooFrequent(s Schedule, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	prev := time.Time{}
	cur := start.Add(-time.Minute)
	deadline := start.AddDate(1, 0, 2)
	for i := 0; i < 4000; i++ {
		next, ok := s.Next(cur, loc)
		if !ok || !next.Before(deadline) {
			return false
		}
		if !prev.IsZero() && next.Sub(prev) < MinInterval {
			return true
		}
		prev = next
		cur = next
	}
	return false
}

// Next returns the soonest fire strictly after t, in loc.
func (s Schedule) Next(t time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	y, m, d := local.Date()
	hh, mm, _ := local.Clock()
	cursor := time.Date(y, m, d, hh, mm, 0, 0, loc)
	if !cursor.After(t) {
		cursor = cursor.Add(time.Minute)
	}
	limit := cursor.AddDate(2, 0, 0)
	for !cursor.After(limit) {
		if s.matches(cursor) {
			return cursor, true
		}
		cursor = cursor.Add(time.Minute)
	}
	return time.Time{}, false
}

// Upcoming returns n fires strictly after t.
func (s Schedule) Upcoming(t time.Time, loc *time.Location, n int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	cur := t
	for len(out) < n {
		next, ok := s.Next(cur, loc)
		if !ok {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

// Between returns fires in (after, until].
func (s Schedule) Between(after, until time.Time, loc *time.Location) []time.Time {
	if until.Before(after) {
		return nil
	}
	var out []time.Time
	cur := after
	for len(out) < 5000 {
		next, ok := s.Next(cur, loc)
		if !ok || next.After(until) {
			break
		}
		out = append(out, next)
		cur = next
	}
	return out
}

func (s Schedule) matches(t time.Time) bool {
	if !contains(s.Months, int(t.Month())) || !contains(s.Hours, t.Hour()) || !contains(s.Minutes, t.Minute()) {
		return false
	}
	domOK := s.DOMStar || contains(s.DOM, t.Day())
	dowOK := s.DOWStar || contains(s.DOW, int(t.Weekday()))
	switch {
	case s.DOMStar && s.DOWStar:
		return true
	case s.DOMStar:
		return dowOK
	case s.DOWStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// Describe returns the plain-language line for a schedule.
func Describe(s Schedule) string {
	if len(s.Minutes) == 1 && len(s.Hours) == 1 && s.DOMStar && len(s.Months) == 12 && sameInts(s.DOW, []int{1, 2, 3, 4, 5}) {
		return "Every weekday at " + clock(s.Hours[0], s.Minutes[0])
	}
	if len(s.Minutes) == 1 && len(s.Hours) == 1 && s.DOMStar && len(s.Months) == 12 && s.DOWStar {
		if len(s.Hours) == 1 && s.Hours[0] >= 0 && everyHour(s) {
			return "Every hour"
		}
		return "Every day at " + clock(s.Hours[0], s.Minutes[0])
	}
	if len(s.Minutes) == 1 && s.Minutes[0] == 0 && len(s.Hours) == 24 && s.DOMStar && s.DOWStar && len(s.Months) == 12 {
		return "Every hour"
	}
	if len(s.Minutes) == 1 && len(s.Hours) == 1 && s.DOMStar && len(s.Months) == 12 && len(s.DOW) == 1 {
		return "Every " + time.Weekday(s.DOW[0]).String() + " at " + clock(s.Hours[0], s.Minutes[0])
	}
	if len(s.Minutes) == 1 && len(s.Hours) == 1 && len(s.DOM) == 1 && s.DOWStar && len(s.Months) == 12 {
		return "On the " + ordinal(s.DOM[0]) + " of every month at " + clock(s.Hours[0], s.Minutes[0])
	}
	if step, ok := minuteStep(s); ok {
		return "Every " + strconv.Itoa(step) + " minutes"
	}
	return "Custom schedule (" + s.Raw + ")"
}

func everyHour(s Schedule) bool {
	return len(s.Hours) == 24 && len(s.Minutes) == 1 && s.Minutes[0] == 0
}

func minuteStep(s Schedule) (int, bool) {
	if !s.DOMStar || !s.DOWStar || len(s.Months) != 12 || len(s.Hours) != 24 || len(s.Minutes) < 2 {
		return 0, false
	}
	step := s.Minutes[1] - s.Minutes[0]
	if step <= 0 || 60%step != 0 {
		return 0, false
	}
	if len(s.Minutes) != 60/step {
		return 0, false
	}
	for i, m := range s.Minutes {
		if m != i*step {
			return 0, false
		}
	}
	return step, true
}

func clock(hour, minute int) string {
	t := time.Date(2026, 1, 1, hour, minute, 0, 0, time.UTC)
	return t.Format("3:04 PM")
}

func ordinal(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return strconv.Itoa(n) + "th"
	}
	switch n % 10 {
	case 1:
		return strconv.Itoa(n) + "st"
	case 2:
		return strconv.Itoa(n) + "nd"
	case 3:
		return strconv.Itoa(n) + "rd"
	default:
		return strconv.Itoa(n) + "th"
	}
}

func parseStarField(expr string, min, max int) ([]int, bool, error) {
	if expr == "*" {
		return span(min, max), true, nil
	}
	vals, err := parseField(expr, min, max)
	return vals, false, err
}

func parseField(expr string, min, max int) ([]int, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("empty field")
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.Split(expr, ",") {
		vals, err := parsePart(part, min, max)
		if err != nil {
			return nil, err
		}
		for _, v := range vals {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty field")
	}
	return uniqueSorted(out), nil
}

func parsePart(expr string, min, max int) ([]int, error) {
	step := 1
	base := expr
	if i := strings.IndexByte(expr, '/'); i >= 0 {
		base = expr[:i]
		n, err := strconv.Atoi(expr[i+1:])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad step")
		}
		step = n
	}
	lo, hi := min, max
	switch {
	case base == "*":
	case strings.Contains(base, "-"):
		bits := strings.Split(base, "-")
		if len(bits) != 2 {
			return nil, fmt.Errorf("bad range")
		}
		a, err1 := strconv.Atoi(bits[0])
		b, err2 := strconv.Atoi(bits[1])
		if err1 != nil || err2 != nil || a > b || a < min || b > max {
			return nil, fmt.Errorf("bad range")
		}
		lo, hi = a, b
	default:
		n, err := strconv.Atoi(base)
		if err != nil || n < min || n > max {
			return nil, fmt.Errorf("bad value")
		}
		if step == 1 {
			return []int{n}, nil
		}
		lo, hi = n, max
	}
	var out []int
	for v := lo; v <= hi; v += step {
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty")
	}
	return out, nil
}

func span(min, max int) []int {
	out := make([]int, 0, max-min+1)
	for v := min; v <= max; v++ {
		out = append(out, v)
	}
	return out
}

func uniqueSorted(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j] < out[j-1] {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

func contains(list []int, v int) bool {
	for _, n := range list {
		if n == v {
			return true
		}
	}
	return false
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
