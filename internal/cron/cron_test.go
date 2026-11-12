package cron

import (
	"strconv"
	"testing"
	"time"
)

func TestParseRangesStepsAndOr(t *testing.T) {
	sched, err := Parse("10-12 8 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := ints(sched.Minutes); got != "10,11,12" {
		t.Fatal(got)
	}
	if got := ints(sched.DOW); got != "1,2,3,4,5" {
		t.Fatal(got)
	}
	step, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := ints(step.Minutes); got != "0,15,30,45" {
		t.Fatal(got)
	}
	part, err := Parse("1-10/3 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := ints(part.Minutes); got != "1,4,7,10" {
		t.Fatal(got)
	}
	// Day-of-month and weekday both set: a day matches either field.
	either, err := Parse("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	sunday := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // not a Monday
	next, ok := either.Next(sunday.Add(-time.Minute), time.UTC)
	if !ok || !next.Equal(sunday) {
		t.Fatalf("dom %v %v", next, ok)
	}
	monday := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) // not the 1st
	next, ok = either.Next(sunday, time.UTC)
	if !ok || !next.Equal(monday) {
		t.Fatalf("dow %v %v", next, ok)
	}
	next, ok = either.Next(monday, time.UTC)
	if !ok || next.Day() == 3 {
		t.Fatalf("skipped a non-match %v", next)
	}
}

func TestParseRejectsBadFields(t *testing.T) {
	for _, expr := range []string{"", "* * *", "60 * * * *", "*/0 * * * *", "1- * * * *"} {
		if _, err := Parse(expr); err == nil {
			t.Fatalf("accepted %q", expr)
		}
	}
}

func TestDescribeFallback(t *testing.T) {
	sched, err := Parse("1 2 3 4 5")
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(sched); got != "Custom schedule (1 2 3 4 5)" {
		t.Fatal(got)
	}
	step, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := Describe(step); got != "Every 15 minutes" {
		t.Fatal(got)
	}
}

func TestTooFrequentBoundary(t *testing.T) {
	loc := time.UTC
	close, err := Parse("*/10 * * * *")
	if err != nil || !TooFrequent(close, loc) {
		t.Fatal(err)
	}
	ok, err := Parse("*/15 * * * *")
	if err != nil || TooFrequent(ok, loc) {
		t.Fatal(err)
	}
	daily, err := Parse("0 8 * * *")
	if err != nil || TooFrequent(daily, loc) {
		t.Fatal(err)
	}
}

func TestNextAcrossSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := Parse("0 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 3, 7, 8, 0, 0, 0, loc)
	next, ok := sched.Next(before, loc)
	if !ok || next.Day() != 8 || next.Hour() != 8 {
		t.Fatalf("next %v %v", next, ok)
	}
	_, offBefore := before.Zone()
	_, offNext := next.Zone()
	if offBefore == offNext {
		t.Fatal("expected the spring-forward offset to change")
	}
}

func ints(vals []int) string {
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += ","
		}
		out += strconv.Itoa(v)
	}
	return out
}
