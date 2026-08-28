// Package runhub is the in-memory event bus for active runs.
//
// The executor publishes log lines, step transitions, and status changes
// while it also persists them; browser SSE connections subscribe here for
// live updates. Subscribers that arrive mid-run get a replay of everything
// published so far, so there are no gaps between page render and stream
// attach. Finished runs are served from the database, not the hub.
package runhub

import (
	"sync"
)

// EventKind discriminates hub events.
type EventKind string

const (
	EventLog    EventKind = "log"
	EventStep   EventKind = "step"
	EventStatus EventKind = "status"
	EventDone   EventKind = "done"
)

// Event is one run update.
type Event struct {
	Kind EventKind
	// Seq is the log sequence number (EventLog only).
	Seq int64
	// Line is the log line (EventLog only).
	Line string
	// StepIdx and StepStatus describe a step transition (EventStep only).
	StepIdx    int
	StepStatus string
	// Status is the run status (EventStatus and EventDone).
	Status string
}

// subscriber buffers events for one SSE connection. If the buffer overflows
// (a very slow client), the channel is closed; the client reconnects and
// replays from its last seen sequence number.
type subscriber struct {
	ch     chan Event
	closed bool
}

type activeRun struct {
	mu     sync.Mutex
	events []Event // full history, replayed to late subscribers
	subs   map[*subscriber]struct{}
	done   bool
}

// Hub tracks all active runs.
type Hub struct {
	mu   sync.Mutex
	runs map[string]*activeRun
}

// New creates an empty hub.
func New() *Hub {
	return &Hub{runs: map[string]*activeRun{}}
}

// Begin registers a run as active. It must be called before any Publish for
// that run.
func (h *Hub) Begin(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs[runID] = &activeRun{subs: map[*subscriber]struct{}{}}
}

// Active reports whether the run is currently registered.
func (h *Hub) Active(runID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.runs[runID]
	return ok
}

// Publish fans an event out to all subscribers and appends it to the replay
// history. Publishing to an unregistered run is a no-op.
func (h *Hub) Publish(runID string, ev Event) {
	h.mu.Lock()
	run := h.runs[runID]
	h.mu.Unlock()
	if run == nil {
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.done {
		return
	}
	run.events = append(run.events, ev)
	for sub := range run.subs {
		sub.send(ev)
	}
}

// End publishes the final status as a done event, closes every subscriber,
// and unregisters the run.
func (h *Hub) End(runID, status string) {
	h.mu.Lock()
	run := h.runs[runID]
	delete(h.runs, runID)
	h.mu.Unlock()
	if run == nil {
		return
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	run.done = true
	ev := Event{Kind: EventDone, Status: status}
	for sub := range run.subs {
		sub.send(ev)
		sub.close()
	}
	run.subs = map[*subscriber]struct{}{}
}

// Subscribe attaches to an active run. It returns the replay of events with
// Seq > afterSeq (non-log events are always replayed), a channel of further
// events, and a cancel function. ok is false when the run is not active —
// the caller should serve the run's final state from the database instead.
func (h *Hub) Subscribe(runID string, afterSeq int64) (replay []Event, ch <-chan Event, cancel func(), ok bool) {
	h.mu.Lock()
	run := h.runs[runID]
	h.mu.Unlock()
	if run == nil {
		return nil, nil, nil, false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.done {
		return nil, nil, nil, false
	}
	for _, ev := range run.events {
		if ev.Kind == EventLog && ev.Seq <= afterSeq {
			continue
		}
		replay = append(replay, ev)
	}
	sub := &subscriber{ch: make(chan Event, 4096)}
	run.subs[sub] = struct{}{}
	cancel = func() {
		run.mu.Lock()
		defer run.mu.Unlock()
		if _, present := run.subs[sub]; present {
			delete(run.subs, sub)
			sub.close()
		}
	}
	return replay, sub.ch, cancel, true
}

func (s *subscriber) send(ev Event) {
	if s.closed {
		return
	}
	select {
	case s.ch <- ev:
	default:
		// Buffer full: drop this subscriber; the SSE client reconnects and
		// resumes from its last event id.
		s.close()
	}
}

func (s *subscriber) close() {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}
