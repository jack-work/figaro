package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
)

func knob(t *testing.T, v string) form.Snapshot {
	t.Helper()
	raw, _ := json.Marshal(v)
	return form.FromMap(map[string]json.RawMessage{FirstByteTimeoutKey: raw})
}

func TestTheFirstByteTimeoutReadsItsKnob(t *testing.T) {
	cases := map[string]time.Duration{"2m": 2 * time.Minute, "0": 0, "off": 0, "nonsense": DefaultFirstByteTimeout, "-1s": DefaultFirstByteTimeout}
	for v, want := range cases {
		if got := FirstByteTimeout(knob(t, v)); got != want {
			t.Errorf("%q: %s, want %s", v, got, want)
		}
	}
	if got := FirstByteTimeout(form.Snapshot{}); got != DefaultFirstByteTimeout {
		t.Errorf("unset: %s", got)
	}
}

func TestARequestWithNoByteBeforeTheDeadlineIsAStreamCut(t *testing.T) {
	ctx, settle := WatchFirstByte(context.Background(), 20*time.Millisecond)
	<-ctx.Done()
	err := settle(ctx.Err())

	cut, ok := AsStreamCut(err)
	if !ok {
		t.Fatalf("got %v, want a StreamCut", err)
	}
	var late *FirstByteTimeoutError
	if cut.Events != 0 || cut.Awaited != "a first byte" || !errors.As(err, &late) {
		t.Errorf("cut = %+v", cut)
	}
}

func TestAByteBeforeTheDeadlineStopsIt(t *testing.T) {
	ctx, settle := WatchFirstByte(context.Background(), 20*time.Millisecond)
	SawByte(ctx)
	time.Sleep(60 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("the deadline fired after a byte arrived: %v", context.Cause(ctx))
	}
	if err := settle(nil); err != nil {
		t.Errorf("settle(nil) = %v", err)
	}
}

func TestAnInterruptedTurnIsNotAFirstByteCut(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, settle := WatchFirstByte(parent, 20*time.Millisecond)
	cancel()
	<-ctx.Done()
	time.Sleep(40 * time.Millisecond)
	err := settle(ctx.Err())
	if _, ok := AsStreamCut(err); ok || !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}
