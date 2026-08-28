package runhub

import (
	"testing"
	"time"
)

func collect(ch <-chan Event, n int, t *testing.T) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timed out after %d events: %+v", len(out), out)
		}
	}
	return out
}

func TestPublishSubscribeReplay(t *testing.T) {
	h := New()
	h.Begin("r1")
	h.Publish("r1", Event{Kind: EventLog, Seq: 1, Line: "one"})
	h.Publish("r1", Event{Kind: EventStep, StepIdx: 0, StepStatus: "running"})

	replay, ch, cancel, ok := h.Subscribe("r1", 0)
	if !ok {
		t.Fatal("expected active run")
	}
	defer cancel()
	if len(replay) != 2 || replay[0].Line != "one" {
		t.Fatalf("replay = %+v", replay)
	}

	h.Publish("r1", Event{Kind: EventLog, Seq: 2, Line: "two"})
	got := collect(ch, 1, t)
	if got[0].Line != "two" {
		t.Fatalf("live = %+v", got)
	}

	h.End("r1", "succeeded")
	final := collect(ch, 1, t)
	if final[0].Kind != EventDone || final[0].Status != "succeeded" {
		t.Fatalf("final = %+v", final)
	}
	if h.Active("r1") {
		t.Fatal("run should be unregistered after End")
	}
}

func TestSubscribeAfterSeqSkipsOldLogs(t *testing.T) {
	h := New()
	h.Begin("r1")
	h.Publish("r1", Event{Kind: EventLog, Seq: 1, Line: "old"})
	h.Publish("r1", Event{Kind: EventStep, StepIdx: 0, StepStatus: "succeeded"})
	h.Publish("r1", Event{Kind: EventLog, Seq: 2, Line: "new"})

	replay, _, cancel, ok := h.Subscribe("r1", 1)
	if !ok {
		t.Fatal("expected active")
	}
	defer cancel()
	if len(replay) != 2 || replay[0].Kind != EventStep || replay[1].Line != "new" {
		t.Fatalf("replay = %+v", replay)
	}
	h.End("r1", "succeeded")
}

func TestSubscribeInactive(t *testing.T) {
	h := New()
	if _, _, _, ok := h.Subscribe("nope", 0); ok {
		t.Fatal("expected inactive")
	}
	h.Begin("r1")
	h.End("r1", "failed")
	if _, _, _, ok := h.Subscribe("r1", 0); ok {
		t.Fatal("expected inactive after end")
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	h := New()
	h.Begin("r1")
	_, ch, cancel, _ := h.Subscribe("r1", 0)
	cancel()
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	// Publishing after cancel must not panic.
	h.Publish("r1", Event{Kind: EventLog, Seq: 1, Line: "x"})
	h.End("r1", "succeeded")
}
