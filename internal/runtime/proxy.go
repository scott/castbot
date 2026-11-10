package runtime

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// DefaultCut is how long the test proxy keeps one response open.
// The MicroVM edge closes a response on that wall clock.
const DefaultCut = 15 * time.Second

// CutProxy forwards a request and ends the response after After.
// A natural end before the deadline is not a cut.
type CutProxy struct {
	Upstream string
	After    time.Duration
	cuts     atomic.Int64
}

// NewCutProxy returns a proxy that cuts each response after the deadline.
// A non-positive after uses DefaultCut.
func NewCutProxy(upstream string, after time.Duration) *CutProxy {
	if after <= 0 {
		after = DefaultCut
	}
	return &CutProxy{Upstream: upstream, After: after}
}

// Cuts is how many responses ended because the deadline fired.
func (p *CutProxy) Cuts() int64 { return p.cuts.Load() }

func (p *CutProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.After)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, p.Upstream+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true, DisableKeepAlives: true}}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			p.cuts.Add(1)
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	buf := make([]byte, 512)
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if ctx.Err() != nil {
				p.cuts.Add(1)
			}
			return
		}
	}
}
