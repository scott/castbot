// Package run drives one agent reply until the runtime sends [DONE].
// The goroutine is not tied to the browser. A cut connection reconnects
// with the data-event offset already consumed.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/rand"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const (
	// Unreachable is the copy shown when the runtime cannot be contacted
	// before any text arrives.
	Unreachable = "The agent is not reachable right now."
	// Stopped is the copy shown when a reply ends before [DONE].
	Stopped = "The reply stopped before it finished."
	// Interrupted is the copy shown when a reply cannot be resumed, or when
	// the process stops while a reply is still running.
	Interrupted = "The reply was interrupted."

	// retryBudget is the total time spent waiting out a transient open:
	// 502, 503, 504, connection refused, connection reset, a dial timeout,
	// a response-header timeout, or a server that accepts and closes
	// before any response bytes. It stays under the runtime's 120s resume TTL.
	retryBudget = 100 * time.Second
	retryMin    = 250 * time.Millisecond
	retryMax    = 8 * time.Second
	// resumeLimit is how many resumes in a row may deliver no new data
	// events and no progress. The next empty resume fails the run.
	// A read that advances the offset clears the count. So does a resume
	// that saw the matching marker and stayed open, which is what a
	// keepalive-only stretch looks like when the edge cuts the socket.
	resumeLimit = 8
	// progressHold is how long a connection must stay open after the
	// matching marker before it counts as progress. A few seconds is
	// enough to tell a held-open keepalive stream from a socket that
	// sends the marker and closes at once. The edge cut is much longer.
	progressHold = 3 * time.Second
	// runLimit is the wall clock cap for one reply. Keepalives can reset
	// the empty-resume streak for a long tool call, and this cap still
	// fails a runtime that never finishes.
	runLimit = 30 * time.Minute
	// DefaultApprovalWait is how long a pending approval stays open.
	// The server can set a different wait. The browser does not.
	DefaultApprovalWait = 10 * time.Minute
)

var (
	errBadResume = errors.New("resume rejected")
	errRunLimit  = errors.New("run time limit")
)

// Notice is a run failure shown as written. It is not retried.
type Notice struct {
	Text string
}

func (n *Notice) Error() string {
	if n == nil {
		return ""
	}
	return n.Text
}

// Job is one reply to stream.
// MessageID is the stored user message. Retries send the same id so the
// runtime session keeps that user turn once.
type Job struct {
	UserID    string
	ThreadID  string
	AgentID   string
	RunID     string
	MessageID string
	Text      string
	SessionID string
	History   []runtime.Turn
}

// Opener opens one runtime stream for a job.
// When it is set, the runner uses it instead of the single client.
type Opener func(ctx context.Context, job Job, req runtime.Send) (runtime.Stream, error)

// Runner starts run goroutines.
type Runner struct {
	ctx      context.Context
	store    *store.Store
	client   runtime.Client
	open     Opener
	Hub      *Hub
	budget   time.Duration
	limit    time.Duration
	approval time.Duration

	mu     sync.Mutex
	active map[string]string // run id to agent id
	rng    *rand.Rand
	// after runs when a reply reaches a terminal status.
	// SetAfterRun chains, so a later slice adds work instead of replacing it.
	after  func(Job, string)
	onPerm func(Job, store.Approval)
}

// New builds a runner. Canceling ctx fails in-flight runs.
func New(ctx context.Context, st *store.Store, client runtime.Client) *Runner {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Runner{
		ctx:      ctx,
		store:    st,
		client:   client,
		Hub:      &Hub{},
		budget:   retryBudget,
		limit:    runLimit,
		approval: DefaultApprovalWait,
		active:   map[string]string{},
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetBudget replaces the transient-error wait. Tests use a short budget.
func (r *Runner) SetBudget(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.budget = d
	r.mu.Unlock()
}

// SetApprovalTimeout sets how long a pending approval stays open.
func (r *Runner) SetApprovalTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.approval = d
	r.mu.Unlock()
}

func (r *Runner) approvalTimeout() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.approval <= 0 {
		return DefaultApprovalWait
	}
	return r.approval
}

// SetRunLimit replaces the wall clock cap for one reply. Tests use a short cap.
func (r *Runner) SetRunLimit(d time.Duration) {
	if d <= 0 {
		return
	}
	r.mu.Lock()
	r.limit = d
	r.mu.Unlock()
}

// SetClient points later opens at a different runtime.
// The call happens before a run starts.
func (r *Runner) SetClient(c runtime.Client) {
	r.mu.Lock()
	r.client = c
	r.mu.Unlock()
}

// SetOpen routes each job. A nil opener keeps the single client.
func (r *Runner) SetOpen(fn Opener) {
	r.mu.Lock()
	r.open = fn
	r.mu.Unlock()
}

func (r *Runner) current() (runtime.Client, Opener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client, r.open
}

func (r *Runner) runLimit() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.limit <= 0 {
		return runLimit
	}
	return r.limit
}

// Done is closed when the runner context is canceled.
func (r *Runner) Done() <-chan struct{} { return r.ctx.Done() }

// Watching reports whether this process still has a goroutine for the run.
func (r *Runner) Watching(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[id]
	return ok
}

// WatchingAgent reports whether this process is streaming a reply for the agent.
// A queued run that was never started is not watched.
func (r *Runner) WatchingAgent(agentID string) bool {
	if agentID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, agent := range r.active {
		if agent == agentID {
			return true
		}
	}
	return false
}

// Wait blocks until in-flight runs have recorded an outcome, or d elapses.
func (r *Runner) Wait(d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		r.mu.Lock()
		n := len(r.active)
		r.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SetAfterRun registers work that runs after a reply is stored or failed.
// A second call chains behind the first, so another slice can register too.
func (r *Runner) SetAfterRun(fn func(Job, string)) {
	r.mu.Lock()
	prev := r.after
	r.after = func(job Job, status string) {
		if prev != nil {
			prev(job, status)
		}
		if fn != nil {
			fn(job, status)
		}
	}
	r.mu.Unlock()
}

// SetOnPermission registers work that runs after a permission card is stored.
// A second call chains behind the first.
func (r *Runner) SetOnPermission(fn func(Job, store.Approval)) {
	r.mu.Lock()
	prev := r.onPerm
	r.onPerm = func(job Job, approval store.Approval) {
		if prev != nil {
			prev(job, approval)
		}
		if fn != nil {
			fn(job, approval)
		}
	}
	r.mu.Unlock()
}

// Start runs the job until the reply is stored or failed.
// A second Start for a run this process already watches does nothing.
func (r *Runner) Start(job Job) {
	if !r.track(job.RunID, job.AgentID) {
		return
	}
	go r.execute(job)
}

func (r *Runner) track(id, agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[id]; ok {
		return false
	}
	r.active[id] = agentID
	return true
}

func (r *Runner) untrack(id string) {
	r.mu.Lock()
	delete(r.active, id)
	r.mu.Unlock()
}

func (r *Runner) execute(job Job) {
	defer r.untrack(job.RunID)
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("run %s panicked", job.RunID)
			r.fail(job, Stopped, 0)
		}
	}()
	if r.ctx.Err() != nil {
		r.fail(job, Interrupted, 0)
		return
	}
	client, opener := r.current()
	if client == nil && opener == nil {
		r.fail(job, Unreachable, 0)
		return
	}
	if err := r.store.MarkStreaming(r.ctx, job.UserID, job.RunID); err != nil {
		if r.ctx.Err() != nil {
			r.fail(job, Interrupted, 0)
			return
		}
		log.Printf("run %s did not start", job.RunID)
		r.fail(job, Stopped, 0)
		return
	}
	r.Hub.Notify(job.RunID)
	var text strings.Builder
	offset := 0
	resumes := 0
	sawMarker := false
	var spent time.Duration
	attempt := 0
	started := time.Now()
	for {
		if r.ctx.Err() != nil || time.Since(started) >= r.runLimit() {
			r.fail(job, Interrupted, offset)
			return
		}
		remain := r.runLimit() - time.Since(started)
		ctx, cancel := context.WithTimeout(r.ctx, remain)
		sessionID := job.ThreadID
		if job.SessionID != "" {
			sessionID = job.SessionID
		}
		send := runtime.Send{
			SessionID: sessionID,
			StreamID:  job.RunID,
			MessageID: job.MessageID,
			Offset:    offset,
			Text:      job.Text,
			History:   job.History,
		}
		var stream runtime.Stream
		var err error
		client, opener = r.current()
		if opener != nil {
			stream, err = opener(ctx, job, send)
		} else if client != nil {
			stream, err = client.Open(ctx, send)
		} else {
			err = errors.New("no runtime")
		}
		if err != nil {
			cancel()
			if r.ctx.Err() != nil || time.Since(started) >= r.runLimit() {
				r.fail(job, Interrupted, offset)
				return
			}
			var note *Notice
			if errors.As(err, &note) && note.Text != "" {
				r.fail(job, note.Text, offset)
				return
			}
			if !transient(err) {
				r.fail(job, giveUp(offset), offset)
				return
			}
			if !r.backoff(&spent, attempt) {
				if r.ctx.Err() != nil {
					r.fail(job, Interrupted, offset)
					return
				}
				r.fail(job, giveUp(offset), offset)
				return
			}
			attempt++
			continue
		}
		attempt = 0
		before := offset
		done, marked, held, rpc, err := r.read(stream, job, &offset, &text, sawMarker, started.Add(r.runLimit()))
		cancel()
		if marked {
			sawMarker = true
		}
		if done {
			if err := r.finish(job, text.String(), offset); err != nil {
				log.Printf("run %s finish failed", job.RunID)
				r.fail(job, Stopped, offset)
			}
			return
		}
		if r.ctx.Err() != nil || errors.Is(err, errRunLimit) {
			r.fail(job, Interrupted, offset)
			return
		}
		if rpc != "" {
			log.Printf("run %s: %s", job.RunID, clip(rpc, 200))
			r.fail(job, clip(rpc, 400), offset)
			return
		}
		if errors.Is(err, errBadResume) {
			r.fail(job, Interrupted, offset)
			return
		}
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			log.Printf("run %s: stream cut", job.RunID)
		}
		// New data clears the empty streak. So does a resume that received
		// the matching marker and stayed open: that is a live runtime
		// sending keepalives, not a connect that closed at once.
		if offset > before || held {
			resumes = 0
		} else {
			resumes++
		}
		if !sawMarker || resumes > resumeLimit {
			r.fail(job, Interrupted, offset)
			return
		}
		if !sleep(r.ctx, 10*time.Millisecond) {
			r.fail(job, Interrupted, offset)
			return
		}
	}
}

func (r *Runner) read(stream runtime.Stream, job Job, offset *int, text *strings.Builder, resuming bool, deadline time.Time) (bool, bool, bool, string, error) {
	defer stream.Close()
	confirmed := false
	var markedAt time.Time
	held := func() bool {
		return confirmed && !markedAt.IsZero() && time.Since(markedAt) >= progressHold
	}
	for {
		frame, err := nextUntil(stream, r.ctx, deadline)
		if err != nil {
			if errors.Is(err, errRunLimit) || errors.Is(err, context.DeadlineExceeded) {
				return false, confirmed, false, "", errRunLimit
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if resuming && !confirmed {
					return false, false, false, "", errBadResume
				}
				return false, confirmed, held(), "", nil
			}
			return false, confirmed, held(), "", err
		}
		if id, ok := markerID(frame.Comment); ok {
			if id == job.RunID {
				confirmed = true
				if markedAt.IsZero() {
					markedAt = time.Now()
				}
			} else if resuming {
				return false, false, false, "", errBadResume
			}
			continue
		}
		if !frame.DataEvent {
			continue
		}
		if resuming && !confirmed {
			return false, false, false, "", errBadResume
		}
		*offset++
		parsed, err := runtime.ParseData(frame.Data)
		if err != nil {
			return false, confirmed, held(), "", err
		}
		if parsed.Done {
			return true, confirmed, held(), "", nil
		}
		if parsed.RPCErr != "" {
			return false, confirmed, held(), parsed.RPCErr, nil
		}
		if len(parsed.Perm) > 0 {
			if err := r.recordPermission(job, parsed.Perm, *offset); err != nil {
				return false, confirmed, held(), "", err
			}
			r.Hub.Notify(job.RunID)
		}
		if parsed.Text == "" {
			continue
		}
		text.WriteString(parsed.Text)
		payload, err := json.Marshal(map[string]string{"text": parsed.Text})
		if err != nil {
			return false, confirmed, held(), "", err
		}
		if _, err := r.store.AppendRunEvent(r.ctx, job.UserID, job.RunID, "text", string(payload), *offset); err != nil {
			return false, confirmed, held(), "", err
		}
		r.Hub.Notify(job.RunID)
	}
}

func nextUntil(stream runtime.Stream, ctx context.Context, deadline time.Time) (runtime.Frame, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Frame{}, err
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return runtime.Frame{}, errRunLimit
	}
	type result struct {
		frame runtime.Frame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		frame, err := stream.Next()
		ch <- result{frame, err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = stream.Close()
		return runtime.Frame{}, ctx.Err()
	case <-timer.C:
		_ = stream.Close()
		return runtime.Frame{}, errRunLimit
	case got := <-ch:
		return got.frame, got.err
	}
}

func (r *Runner) recordPermission(job Job, raw json.RawMessage, offset int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	approval, err := r.store.RecordPermission(ctx, job.UserID, job.RunID, raw, r.store.Now().Add(r.approvalTimeout()), offset)
	if err != nil {
		return err
	}
	r.mu.Lock()
	fn := r.onPerm
	r.mu.Unlock()
	if fn != nil {
		fn(job, approval)
	}
	return nil
}

// Finish records a successful run and notifies after-run hooks.
// The streaming path uses the same function.
func (r *Runner) Finish(job Job, text string, offset int) error {
	return r.finish(job, text, offset)
}

func (r *Runner) finish(job Job, text string, offset int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := r.store.FinishRun(ctx, job.UserID, job.RunID, text, offset); err != nil {
		return err
	}
	r.Hub.Notify(job.RunID)
	r.notifyAfter(job, "done")
	return nil
}

func (r *Runner) fail(job Job, message string, offset int) {
	message = clip(message, 400)
	if message == "" {
		message = Stopped
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := r.store.FailRun(ctx, job.UserID, job.RunID, message, offset); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("run %s fail failed", job.RunID)
		}
		return
	}
	r.Hub.Notify(job.RunID)
	r.notifyAfter(job, "failed")
}

func (r *Runner) notifyAfter(job Job, status string) {
	// Drop the watch before the hook. The run is already finished, and the hook
	// starts the next reply. Leaving this run watched would park that start.
	r.untrack(job.RunID)
	r.mu.Lock()
	fn := r.after
	r.mu.Unlock()
	if fn != nil {
		fn(job, status)
	}
}

func (r *Runner) backoff(spent *time.Duration, attempt int) bool {
	r.mu.Lock()
	budget := r.budget
	r.mu.Unlock()
	if budget <= 0 || *spent >= budget {
		return false
	}
	delay := retryMin
	for i := 0; i < attempt && delay < retryMax; i++ {
		delay *= 2
	}
	if delay > retryMax {
		delay = retryMax
	}
	r.mu.Lock()
	span := int64(delay / 5)
	var jitter int64
	if span > 0 {
		jitter = r.rng.Int63n(span*2+1) - span
	}
	r.mu.Unlock()
	delay += time.Duration(jitter)
	if delay < 50*time.Millisecond {
		delay = 50 * time.Millisecond
	}
	remain := budget - *spent
	if delay > remain {
		delay = remain
	}
	if delay <= 0 {
		return false
	}
	if !sleep(r.ctx, delay) {
		return false
	}
	*spent += delay
	return true
}

func transient(err error) bool {
	var status *runtime.StatusError
	if errors.As(err, &status) {
		return status.Code == 502 || status.Code == 503 || status.Code == 504
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	// A server that accepts the socket and then closes it, with no response
	// bytes and no TCP reset, surfaces as EOF. That is the same kind of
	// blip as a gateway 502.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func giveUp(offset int) string {
	if offset == 0 {
		return Unreachable
	}
	return Stopped
}

func markerID(comment string) (string, bool) {
	const prefix = "cast-stream "
	if !strings.HasPrefix(comment, prefix) {
		return "", false
	}
	id := strings.TrimSpace(strings.TrimPrefix(comment, prefix))
	if id == "" {
		return "", false
	}
	return id, true
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
