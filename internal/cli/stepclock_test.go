package cli

import (
	"reflect"
	"testing"
	"time"
)

// The step clock fires in due order, ties in the order they were set, moves
// only when the stepper moves it, and a ticker computes each due time from
// its period so it never drifts off the frame grid.
func TestTheStepClockFiresInDueOrderAndOnlyWhenStepped(t *testing.T) {
	c := newStepClock(time.Unix(1000, 0))
	var fired []string
	c.AfterFunc(30*time.Millisecond, func() { fired = append(fired, "b") })
	c.AfterFunc(10*time.Millisecond, func() { fired = append(fired, "a") })
	c.AfterFunc(30*time.Millisecond, func() { fired = append(fired, "c") })
	stop := c.AfterFunc(20*time.Millisecond, func() { fired = append(fired, "stopped") })
	if !stop() {
		t.Fatal("stopping a pending timer reported it already gone")
	}
	for c.fireOne(25 * time.Millisecond) {
	}
	if !reflect.DeepEqual(fired, []string{"a"}) {
		t.Fatalf("by 25ms fired %v", fired)
	}
	for c.fireOne(30 * time.Millisecond) {
	}
	if !reflect.DeepEqual(fired, []string{"a", "b", "c"}) {
		t.Fatalf("by 30ms fired %v", fired)
	}
	if got := c.Now(); !got.Equal(time.Unix(1000, 0).Add(30 * time.Millisecond)) {
		t.Fatalf("the clock reads %v after firing at 30ms", got)
	}
}

func TestAStepClockTickerLandsOnTheFrameGrid(t *testing.T) {
	c := newStepClock(time.Time{})
	var at []time.Duration
	c.Every(time.Second/12, func() { at = append(at, c.elapsed()) })
	for n := uint64(1); n <= 600; n++ {
		step := time.Duration(int64(n) * int64(time.Second) / 60)
		for c.fireOne(step) {
		}
		c.advance(step)
		if len(at) != int(n/5) {
			t.Fatalf("by step %d the 12 fps ticker has ticked %d times, want %d: off the grid", n, len(at), n/5)
		}
	}
}
