//go:build shots

package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// shotWaitDefault is the per-condition budget for a screenshot wait.
// Polls return as soon as the condition is true, so a passing run does not
// spend this time. CASTBOT_SHOT_WAIT overrides it (Go duration, 5s to 2m).
const shotWaitDefault = 45 * time.Second

// pageReadyExpr is true once the document and its web fonts have loaded.
// Live chat streams and the held wake request stay open on purpose, so this
// does not wait for an idle network.
const pageReadyExpr = `(function(){if(document.readyState!=='complete')return false;if(document.fonts&&document.fonts.status!=='loaded')return false;return true;})()`

var shotRetryUsed sync.Map

func parseShotWait(v string) (time.Duration, error) {
	if v == "" {
		return shotWaitDefault, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("CASTBOT_SHOT_WAIT %q: %w", v, err)
	}
	if d < 5*time.Second || d > 2*time.Minute {
		return 0, fmt.Errorf("CASTBOT_SHOT_WAIT %q is outside 5s..2m", v)
	}
	return d, nil
}

func shotWait() time.Duration {
	d, err := parseShotWait(os.Getenv("CASTBOT_SHOT_WAIT"))
	if err != nil {
		panic(err)
	}
	return d
}

// claimShotRetry allows one timeout retry for this subtest. A second timeout
// fails, so a real regression is not hidden behind a loop of retries.
func claimShotRetry(t *testing.T) bool {
	_, loaded := shotRetryUsed.LoadOrStore(t.Name(), true)
	return !loaded
}

func withShotRetry(t *testing.T, label string, once func() bool) bool {
	t.Helper()
	if once() {
		return true
	}
	if !claimShotRetry(t) {
		return false
	}
	t.Logf("shot wait timed out after %s for %s; retrying this shot's wait once", shotWait(), label)
	if once() {
		t.Logf("shot wait retry succeeded for %s", label)
		return true
	}
	return false
}

func runPoll(ctx context.Context, expr string, d time.Duration) error {
	// A timer keeps checking on a busy runner. Animation frames can stall there.
	return chromedp.Poll(expr, nil, chromedp.WithPollingInterval(100*time.Millisecond), chromedp.WithPollingTimeout(d)).Do(ctx)
}

func pollTimedOut(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, chromedp.ErrPollingTimeout.Error()) || strings.Contains(msg, "context deadline exceeded")
}

func shotPoll(t *testing.T, label, expr string) chromedp.Action {
	return shotPollFor(t, label, expr, shotWait())
}

func shotPollFor(t *testing.T, label, expr string, d time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		err := runPoll(ctx, expr, d)
		retried := false
		if err != nil && pollTimedOut(err) && ctx.Err() == nil && claimShotRetry(t) {
			retried = true
			t.Logf("shot wait timed out after %s for %s; retrying this shot's wait once", d, label)
			err = runPoll(ctx, expr, d)
			if err == nil {
				t.Logf("shot wait retry succeeded for %s", label)
				return nil
			}
		}
		if err == nil {
			return nil
		}
		kind := "failed"
		if pollTimedOut(err) {
			kind = "timed out"
		}
		note := ""
		if retried {
			note = " (retried once)"
		}
		return fmt.Errorf("wait %s %s after %s%s: %v\ncondition: %s\n%s", label, kind, d, note, err, squash(expr), pageSnap(ctx))
	})
}

func pageReady(t *testing.T) chromedp.Action {
	return shotPoll(t, "document ready and fonts loaded", pageReadyExpr)
}

func shotVisible(t *testing.T, sel string, opts ...chromedp.QueryOption) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		err := visibleOnce(ctx, sel, opts...)
		retried := false
		if err != nil && pollTimedOut(err) && ctx.Err() == nil && claimShotRetry(t) {
			retried = true
			t.Logf("shot wait timed out after %s for selector %s; retrying this shot's wait once", shotWait(), sel)
			err = visibleOnce(ctx, sel, opts...)
			if err == nil {
				t.Logf("shot wait retry succeeded for selector %s", sel)
				return nil
			}
		}
		if err == nil {
			return nil
		}
		note := ""
		if retried {
			note = " (retried once)"
		}
		return fmt.Errorf("wait for visible selector %q after %s%s: %v\n%s", sel, shotWait(), note, err, pageSnap(ctx))
	})
}

func visibleOnce(ctx context.Context, sel string, opts ...chromedp.QueryOption) error {
	wctx, cancel := context.WithTimeout(ctx, shotWait())
	defer cancel()
	return chromedp.WaitVisible(sel, opts...).Do(wctx)
}

func pageSnap(ctx context.Context) string {
	var snap struct {
		URL   string `json:"url"`
		Ready string `json:"ready"`
		Fonts string `json:"fonts"`
		Text  string `json:"text"`
	}
	err := chromedp.Evaluate(js(`(function(){
		var t = (document.body && document.body.innerText) || '';
		t = t.replace(/\s+/g, ' ').slice(0, 400);
		var fonts = '';
		try { fonts = (document.fonts && document.fonts.status) || ''; } catch (e) { fonts = ''; }
		return {url: location.pathname + location.search, ready: document.readyState || '', fonts: fonts, text: t};
	})()`), &snap).Do(ctx)
	if err != nil {
		return "page snapshot unavailable: " + err.Error()
	}
	return fmt.Sprintf("url: %s\nready: %s fonts: %s\ntext: %s", snap.URL, snap.Ready, snap.Fonts, snap.Text)
}

func squash(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 240
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
