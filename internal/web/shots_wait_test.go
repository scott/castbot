//go:build shots

package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestParseShotWait(t *testing.T) {
	d, err := parseShotWait("")
	if err != nil || d != 45*time.Second {
		t.Fatalf("default %v %v", d, err)
	}
	d, err = parseShotWait("30s")
	if err != nil || d != 30*time.Second {
		t.Fatalf("30s %v %v", d, err)
	}
	d, err = parseShotWait("1m")
	if err != nil || d != time.Minute {
		t.Fatalf("1m %v %v", d, err)
	}
	if _, err = parseShotWait("4s"); err == nil {
		t.Fatal("short value accepted")
	}
	if _, err = parseShotWait("3m"); err == nil {
		t.Fatal("long value accepted")
	}
	if _, err = parseShotWait("nope"); err == nil {
		t.Fatal("bad value accepted")
	}
}

func TestPollTimedOut(t *testing.T) {
	if pollTimedOut(nil) {
		t.Fatal("nil")
	}
	if !pollTimedOut(chromedp.ErrPollingTimeout) {
		t.Fatal("poll")
	}
	if !pollTimedOut(errors.New("waiting for function failed: timeout")) {
		t.Fatal("text")
	}
	if !pollTimedOut(context.DeadlineExceeded) {
		t.Fatal("deadline")
	}
	if pollTimedOut(context.Canceled) {
		t.Fatal("cancel")
	}
}

func TestClaimShotRetryOnce(t *testing.T) {
	t.Run("a", func(t *testing.T) {
		if !claimShotRetry(t) || claimShotRetry(t) {
			t.Fatal("one retry")
		}
	})
	t.Run("b", func(t *testing.T) {
		if !claimShotRetry(t) {
			t.Fatal("other shot")
		}
	})
}

func TestSquashKeepsConditionShort(t *testing.T) {
	got := squash("  a \n b\t c  ")
	if got != "a b c" {
		t.Fatal(got)
	}
	long := strings.Repeat("word ", 80)
	got = squash(long)
	if len(got) > 243 || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d %q", len(got), got[len(got)-10:])
	}
}
