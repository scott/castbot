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
	// or a response-header timeout. It stays under the runtime's 120s resume TTL.
	retryBudget = 100 * time.Second
	retryMin    = 250 * time.Millisecond
	retryMax    = 8 * time.Second
	// resumeLimit is how many resumes in a row may deliver no new data
	// events. The next empty resume fails the run. A read that advances
	// the offset clears the count, so a long healthy reply can be cut
	// many times.
	resumeLimit = 8
)

var errBadResume = errors.New("resume rejected")

// Job is one reply to stream.
type Job struct {
	UserID   string
	ThreadID string
	AgentID  string
	RunID    string
	Text     string
	History  []runtime.Turn
}

// Runner starts run goroutines.
type Runner struct {
	ctx    context.Context
	store  *store.Store
	client runtime.Client
	Hub    *Hub
	budget time.Duration

	mu     sync.Mutex
	active map[string]struct{}
	rng    *rand.Rand
}

// New builds a runner. Canceling ctx fails in-flight runs.
func New(ctx context.Context, st *store.Store, client runtime.Client) *Runner {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Runner{
		ctx:    ctx,
		store:  st,
		client: client,
		Hub:    &Hub{},
		budget: retryBudget,
		active: map[string]struct{}{},
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
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

// Done is closed when the runner context is canceled.
func (r *Runner) Done() <-chan struct{} { return r.ctx.Done() }

// Watching reports whether this process still has a goroutine for the run.
func (r *Runner) Watching(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[id]
	return ok
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

// Start runs the job until the reply is stored or failed.
func (r *Runner) Start(job Job) {
	r.track(job.RunID)
	go r.execute(job)
}

func (r *Runner) track(id string) {
	r.mu.Lock()
	r.active[id] = struct{}{}
	r.mu.Unlock()
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
	if r.client == nil {
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
	for {
		if r.ctx.Err() != nil {
			r.fail(job, Interrupted, offset)
			return
		}
		stream, err := r.client.Open(r.ctx, runtime.Send{
			SessionID: job.ThreadID,
			StreamID:  job.RunID,
			Offset:    offset,
			Text:      job.Text,
			History:   job.History,
		})
		if err != nil {
			if r.ctx.Err() != nil {
				r.fail(job, Interrupted, offset)
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
		done, marked, rpc, err := r.read(stream, job, &offset, &text, sawMarker)
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
		if r.ctx.Err() != nil {
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
		// A read that stored new data events clears the empty streak.
		// Only resumes that deliver no new bytes count toward the cap.
		if offset > before {
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

func (r *Runner) read(stream runtime.Stream, job Job, offset *int, text *strings.Builder, resuming bool) (bool, bool, string, error) {
	defer stream.Close()
	confirmed := false
	for {
		frame, err := stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				if resuming && !confirmed {
					return false, false, "", errBadResume
				}
				return false, confirmed, "", nil
			}
			return false, confirmed, "", err
		}
		if id, ok := markerID(frame.Comment); ok {
			if id == job.RunID {
				confirmed = true
			} else if resuming {
				return false, false, "", errBadResume
			}
			continue
		}
		if !frame.DataEvent {
			continue
		}
		if resuming && !confirmed {
			return false, false, "", errBadResume
		}
		*offset++
		parsed, err := runtime.ParseData(frame.Data)
		if err != nil {
			return false, confirmed, "", err
		}
		if parsed.Done {
			return true, confirmed, "", nil
		}
		if parsed.RPCErr != "" {
			return false, confirmed, parsed.RPCErr, nil
		}
		if len(parsed.Perm) > 0 {
			payload, err := json.Marshal(map[string]json.RawMessage{"permission": parsed.Perm})
			if err != nil {
				return false, confirmed, "", err
			}
			if _, err := r.store.AppendRunEvent(r.ctx, job.UserID, job.RunID, "permission", string(payload), *offset); err != nil {
				return false, confirmed, "", err
			}
			r.Hub.Notify(job.RunID)
		}
		if parsed.Text == "" {
			continue
		}
		text.WriteString(parsed.Text)
		payload, err := json.Marshal(map[string]string{"text": parsed.Text})
		if err != nil {
			return false, confirmed, "", err
		}
		if _, err := r.store.AppendRunEvent(r.ctx, job.UserID, job.RunID, "text", string(payload), *offset); err != nil {
			return false, confirmed, "", err
		}
		r.Hub.Notify(job.RunID)
	}
}

func (r *Runner) finish(job Job, text string, offset int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := r.store.FinishRun(ctx, job.UserID, job.RunID, text, offset); err != nil {
		return err
	}
	r.Hub.Notify(job.RunID)
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
