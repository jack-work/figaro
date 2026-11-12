package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

const cancelTestTurns = 6

// A conversation whose form moves on every turn, with one change landing
// mid-stream and one record that encodes to nothing: the shapes a resumed
// derivation has to seed correctly.
func cancelTestLog(t *testing.T) (store.Log[message.Message], Form) {
	t.Helper()
	log := store.NewMemLog[message.Message]()
	var bound versionedForm
	version := uint64(0)
	for turn := range cancelTestTurns {
		n := strconv.Itoa(turn)
		bound.patches = append(bound.patches, setKey("mantra", "turn "+n))
		version++
		ask := "ask " + n
		if turn == 3 {
			ask = "silent"
		}
		call := "c" + n
		for _, e := range []store.Entry[message.Message]{
			{FormChannelVersion: version, Payload: message.Message{Role: message.RoleInput,
				Content: []message.Content{message.TextContent(ask)}}},
			{FormChannelVersion: version + 1, Payload: message.Message{Role: message.RoleOutput, StopReason: message.StopToolInvoke,
				Content: []message.Content{{Type: message.ContentToolInvoke, ToolCallID: call, ToolName: "bash"}}}},
			{FormChannelVersion: version + 1, Payload: message.Message{Role: message.RoleInput,
				Content: []message.Content{message.ToolResultContent(call, "bash", "ok "+n, false)}}},
		} {
			if _, err := log.Append(e); err != nil {
				t.Fatal(err)
			}
		}
		bound.patches = append(bound.patches, setKey("during", "stream "+n))
		version++
	}
	return log, bound
}

func cancelTestConfig(log store.Log[message.Message], bound Form, rows store.Log[[]json.RawMessage]) CatchUpConfig {
	return CatchUpConfig{
		Log:         log,
		Translator:  rows,
		Form:        bound,
		Fingerprint: "v1",
		Encode: func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			if len(msg.Content) > 0 && msg.Content[0].Text == "silent" {
				return nil, nil
			}
			seen := map[string]string{}
			for _, key := range []string{"mantra", "during"} {
				if v := snap.Lookup(key); v != nil {
					seen[key] = *v
				}
			}
			body, err := json.Marshal(map[string]any{
				"role": string(msg.Role), "lt": msg.LogicalTime, "form": seen, "patches": msg.Patches,
			})
			if err != nil {
				return nil, err
			}
			return []json.RawMessage{body}, nil
		},
	}
}

func rowBytes(t *testing.T, rows store.Log[[]json.RawMessage]) string {
	t.Helper()
	b, err := json.Marshal(rows.Read())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A drain cancels the turn's context. Catch-up must stop at the next record
// instead of encoding a history nobody will send, and what it leaves must be
// a prefix the next catch-up completes to the bytes an uninterrupted one
// writes: the rows are a cache the model's prompt cache is keyed on.
func TestACancelledCatchUpStopsAndResumesToTheSameRows(t *testing.T) {
	log, bound := cancelTestLog(t)
	whole := store.NewMemLog[[]json.RawMessage]()
	full, err := CatchUp(cancelTestConfig(log, bound, whole))
	if err != nil {
		t.Fatal(err)
	}
	want := rowBytes(t, whole)
	if full.Unwritten != 1 || full.Encoded != full.Visited-1 {
		t.Fatalf("uninterrupted pass: %+v, want one empty row and the rest encoded", full)
	}

	for stopAfter := 0; stopAfter < full.Visited; stopAfter++ {
		rows := store.NewMemLog[[]json.RawMessage]()
		ctx, cancel := context.WithCancel(context.Background())
		cfg := cancelTestConfig(log, bound, rows)
		cfg.Context = ctx
		encode, calls := cfg.Encode, 0
		cfg.Encode = func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			if calls++; calls == stopAfter {
				cancel()
			}
			return encode(msg, snap)
		}
		if stopAfter == 0 {
			cancel()
		}

		_, err := CatchUp(cfg)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stop after %d: err=%v, want context.Canceled", stopAfter, err)
		}
		if calls != stopAfter {
			t.Fatalf("stop after %d: %d records were encoded after the cancel", stopAfter, calls-stopAfter)
		}
		if got := rows.Len(); got != stopAfter {
			t.Fatalf("stop after %d: %d rows written, want the %d encoded before the cancel", stopAfter, got, stopAfter)
		}

		if _, err := CatchUp(cancelTestConfig(log, bound, rows)); err != nil {
			t.Fatalf("stop after %d: resume: %v", stopAfter, err)
		}
		if got := rowBytes(t, rows); got != want {
			t.Fatalf("stop after %d: resumed rows differ from an uninterrupted catch-up\n got %s\nwant %s", stopAfter, got, want)
		}
	}
}

// Without a context catch-up runs to the end, as every caller outside a turn does.
func TestCatchUpWithoutAContextRunsToTheEnd(t *testing.T) {
	log, bound := cancelTestLog(t)
	rows := store.NewMemLog[[]json.RawMessage]()
	stats, err := CatchUp(cancelTestConfig(log, bound, rows))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Visited != 3*cancelTestTurns || rows.Len() != stats.Visited {
		t.Fatalf("visited=%d rows=%d, want %d of each", stats.Visited, rows.Len(), 3*cancelTestTurns)
	}
}
