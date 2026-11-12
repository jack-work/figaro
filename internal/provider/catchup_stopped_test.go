package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// An interrupted turn ends as "interrupted" and its provider's error is not
// logged, so a catch-up that a cancel cut short left no trace of itself but
// the rows. It now says so once: a trace event, and a line naming the
// provider, the record it stopped before and the rows it wrote. A catch-up
// that runs to its end says neither.
func TestACancelledCatchUpSaysWhereItStopped(t *testing.T) {
	var logged bytes.Buffer
	before := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(before) })

	run := func(stopAfter int) (phases []string, rows store.Log[[]json.RawMessage], err error) {
		log, bound := cancelTestLog(t)
		rows = store.NewMemLog[[]json.RawMessage]()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := cancelTestConfig(log, bound, rows)
		cfg.Context = ctx
		cfg.Phase = func(name string) { phases = append(phases, name) }
		cfg.ReportStopped = LogCatchUpStopped("anthropic")
		encode, calls := cfg.Encode, 0
		cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			if calls++; calls == stopAfter {
				cancel()
			}
			return encode(msg, snap)
		}
		_, err = CatchUp(cfg)
		return phases, rows, err
	}
	count := func(names []string, want string) (n int) {
		for _, name := range names {
			if name == want {
				n++
			}
		}
		return n
	}
	const line = "catch-up stopped: the turn was cancelled"

	phases, rows, err := run(0)
	if err != nil {
		t.Fatal(err)
	}
	if count(phases, "stopped") != 0 || strings.Contains(logged.String(), line) {
		t.Fatalf("a catch-up that ran to its end reported a stop: phases %v, log %q", phases, logged.String())
	}
	if rows.Len() != 3*cancelTestTurns {
		t.Fatalf("%d rows, want %d", rows.Len(), 3*cancelTestTurns)
	}

	logged.Reset()
	phases, rows, err = run(5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if got := count(phases, "stopped"); got != 1 {
		t.Fatalf("the stopped event was traced %d times, want once: %v", got, phases)
	}
	out := logged.String()
	if strings.Count(out, line) != 1 {
		t.Fatalf("the stop was logged %d times, want once: %q", strings.Count(out, line), out)
	}
	for _, want := range []string{"level=INFO", "provider=anthropic", "before_lt=6", "rows_written=5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the line lacks %s: %q", want, out)
		}
	}
	if rows.Len() != 5 {
		t.Fatalf("%d rows written, want the 5 the line reports", rows.Len())
	}
}
