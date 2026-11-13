package web

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
)

// liveSend is one browser's view of a run.
// Act and tools frames are skipped when the HTML matches what this
// stream already sent. The fold grows with each new event so a chunk
// does not read the whole history again.
type liveSend struct {
	run        store.Run
	pending    bool
	machine    string
	fold       runFold
	lastTools  string
	actReady   bool
	actHidden  bool
	actCopy    string
	lastTail   string
	lastTailAt time.Time
	heldTail   string
}

// tailGap caps live markdown tail swaps at about 13 frames a second.
// A block, a tool, an approval, or the end of the run flushes sooner.
const tailGap = 75 * time.Millisecond

// runFold is the text and tool rows seen so far on one stream.
type runFold struct {
	text  strings.Builder
	tools strings.Builder
	fact  toolFact
}

func (f *runFold) apply(ev store.RunEvent) (before string) {
	before = f.text.String()
	switch ev.Kind {
	case "text":
		var payload struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ev.Payload), &payload) == nil {
			f.text.WriteString(payload.Text)
		}
	case "tool":
		got := parseTool(ev.Payload)
		if got.Kind == "" {
			return before
		}
		f.fact = got
		f.tools.WriteString(toolRowHTML(got))
	}
	return before
}

func (f *runFold) body() string { return f.text.String() }

func (f *runFold) toolsHTML() string { return f.tools.String() }

// primeLive loads the run once and folds events the page already showed.
// Later chunks append to that fold and reuse the cached run and approvals.
func (s *Server) primeLive(ctx context.Context, user store.User, st *liveSend, cur resumePos) error {
	if err := s.refreshLive(ctx, user, st); err != nil {
		return err
	}
	if cur.listAfter() > 0 {
		events, err := s.store.ListRunEvents(ctx, user.ID, st.run.ID, 0)
		if err != nil {
			return err
		}
		upto := cur.listAfter()
		for _, ev := range events {
			if ev.Seq <= upto {
				st.fold.apply(ev)
			}
		}
	}
	out, elapsed := s.activityNow(st)
	st.noteAct(out, elapsed)
	st.lastTools = st.fold.toolsHTML()
	return nil
}

// refreshLive reads the run, pending approvals, and machine phase once
// per loop, not once per text chunk.
func (s *Server) refreshLive(ctx context.Context, user store.User, st *liveSend) error {
	row, err := s.store.GetRun(ctx, user.ID, st.run.ID)
	if err != nil {
		return err
	}
	st.run = row
	pending := row.Status == "waiting_approval"
	if !pending {
		rows, err := s.store.ListRunApprovals(ctx, user.ID, row.ID)
		if err != nil {
			return err
		}
		for _, ap := range rows {
			if ap.Status == "pending" {
				pending = true
				break
			}
		}
	}
	st.pending = pending
	st.machine = s.machinePhase(ctx, user.ID, row.AgentID)
	return nil
}

func (s *Server) activityNow(st *liveSend) (activityOut, string) {
	text := st.fold.body()
	in := activityIn{
		Status:          st.run.Status,
		HasText:         strings.TrimSpace(text) != "",
		PendingApproval: st.pending || st.run.Status == "waiting_approval",
		Machine:         st.machine,
	}
	if st.fold.fact.Open && st.fold.fact.Kind != "" {
		in.ToolKind = st.fold.fact.Kind
	}
	out := decideActivity(in)
	if !out.HideThread {
		shown := dwellLabel(st.run.ID, out.Label, s.store.Now())
		if shown != out.Label {
			out.Label = shown
			out.Mode = modeFor(shown)
			out.Hint = ""
		}
	}
	elapsed := ""
	if started, err := time.Parse(time.RFC3339, st.run.StartedAt); err == nil {
		elapsed, _ = formatElapsed(s.store.Now().Sub(started))
	}
	return out, elapsed
}

// noteAct records the row already painted on the page.
// The stream does not send that HTML again.
func (st *liveSend) noteAct(out activityOut, elapsed string) {
	st.actReady = true
	st.actHidden = out.HideThread
	st.actCopy = actCopyHTML(out, elapsed)
}

// putAct sends a full row only when the row appears or disappears.
// A label or clock change swaps the inner text and leaves the row mounted.
func (st *liveSend) putAct(fw *frameWriter, w io.Writer, out activityOut, elapsed string) error {
	copy := actCopyHTML(out, elapsed)
	if st.actReady && st.actHidden == out.HideThread && st.actCopy == copy {
		return nil
	}
	write := func(event, data string) error {
		if fw != nil {
			return fw.write(event, data)
		}
		return writeSSE(w, "", event, data)
	}
	if st.actReady && st.actHidden == out.HideThread {
		if err := write("actxt", copy); err != nil {
			return err
		}
	} else {
		slot := actSlotHTML(st.run.ID, actRowHTML(out, elapsed), out.HideThread)
		if err := write("act", slot); err != nil {
			return err
		}
	}
	st.actReady = true
	st.actHidden = out.HideThread
	st.actCopy = copy
	return nil
}

// syncActivity keeps the clock and the label moving between chunks.
func (s *Server) syncActivity(w io.Writer, st *liveSend) error {
	out, elapsed := s.activityNow(st)
	return st.putAct(nil, w, out, elapsed)
}

// emitTail sends the open markdown immediately on the first frame and on
// a block, tool, or approval. Other text chunks are held so the tail is
// swapped at most once per tailGap.
func (st *liveSend) emitTail(w io.Writer, inner string, immediate bool) error {
	if !immediate && !st.lastTailAt.IsZero() && time.Since(st.lastTailAt) < tailGap {
		if inner == st.lastTail {
			st.heldTail = ""
		} else {
			st.heldTail = inner
		}
		return nil
	}
	return st.sendTail(w, inner)
}

// flushTail writes a tail that was held for the cadence window.
func (st *liveSend) flushTail(w io.Writer) error {
	if st.heldTail == "" {
		return nil
	}
	return st.sendTail(w, st.heldTail)
}

func (st *liveSend) sendTail(w io.Writer, inner string) error {
	st.heldTail = ""
	if inner == st.lastTail {
		return nil
	}
	if err := writeSSE(w, "", "tail", inner); err != nil {
		return err
	}
	st.lastTail = inner
	st.lastTailAt = time.Now()
	return nil
}

// tailDelay shortens the stream wait so a held tail is not stuck behind
// the normal poll interval.
func (st *liveSend) tailDelay(max time.Duration) time.Duration {
	if st.heldTail == "" || st.lastTailAt.IsZero() {
		return max
	}
	left := tailGap - time.Since(st.lastTailAt)
	if left <= 0 {
		return 0
	}
	if left < max {
		return left
	}
	return max
}
