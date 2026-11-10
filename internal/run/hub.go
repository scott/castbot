package run

import "sync"

// Hub wakes browser subscribers after a run event is stored.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// Subscribe returns a buffered signal for one run and a cancel func.
func (h *Hub) Subscribe(runID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[string]map[chan struct{}]struct{}{}
	}
	set := h.subs[runID]
	if set == nil {
		set = map[chan struct{}]struct{}{}
		h.subs[runID] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[runID], ch)
		if len(h.subs[runID]) == 0 {
			delete(h.subs, runID)
		}
		h.mu.Unlock()
	}
}

// Notify wakes current subscribers. A subscriber that is not waiting keeps
// one signal so the next read still sees the new rows.
func (h *Hub) Notify(runID string) {
	h.mu.Lock()
	set := h.subs[runID]
	list := make([]chan struct{}, 0, len(set))
	for ch := range set {
		list = append(list, ch)
	}
	h.mu.Unlock()
	for _, ch := range list {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
