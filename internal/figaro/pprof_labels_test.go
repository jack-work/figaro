package figaro

import (
	"bytes"
	"context"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// A hung daemon is diagnosed from a goroutine dump, and an unlabeled dump
// cannot answer the only question worth asking: WHICH aria is stuck. This
// test asserts the label reaches the profile, and that it is inherited by
// goroutines the agent spawns - the turn loop and the provider stream reader
// are children, and they are where a hang actually lives.
func TestAgentGoroutineLabelsCarryAriaID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const ariaID = "94f0752b"
	started := make(chan struct{})
	childStarted := make(chan struct{})

	go pprof.Do(ctx, pprof.Labels("figaro.aria", ariaID), func(ctx context.Context) {
		// A child goroutine, as the turn loop and tool dispatch are.
		go func() {
			close(childStarted)
			<-ctx.Done()
		}()
		close(started)
		<-ctx.Done()
	})

	<-started
	<-childStarted
	// Give the child a moment to be observable in the profile.
	time.Sleep(10 * time.Millisecond)

	var buf bytes.Buffer
	// debug=1 is the format that renders labels; debug=2 (the full stack
	// dump) drops them, which is worth knowing before you go looking.
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 1); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, ariaID) {
		t.Fatalf("goroutine profile carries no aria label; a dump of this daemon is unattributable")
	}
	if n := strings.Count(got, ariaID); n < 2 {
		t.Errorf("label appears %d time(s); it must be inherited by child goroutines too", n)
	}
}
