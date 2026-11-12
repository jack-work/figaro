package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

type compactedForm struct{ state form.Snapshot }

func (compactedForm) PatchesBetween(after, upTo uint64) []message.Patch { return nil }
func (c compactedForm) StateAt(uint64) (form.Snapshot, bool)            { return c.state, true }

// A bound form born under a compacted outfit cannot replay its patches from
// version 1; a resumed derivation must seed from the state at its cursor, or
// every message after the resume renders against an empty form.
func TestASeedReadsTheFormStateNotItsWholeHistory(t *testing.T) {
	state := form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"opus"`)}, nil))
	log := store.NewMemLog[message.Message]()
	e, err := log.Append(store.Entry[message.Message]{
		FormChannelVersion: 40,
		Payload:            message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDeriver(compactedForm{state: state}, nil)
	d.SeedAt(log, rowsThrough(t, e.LT))
	if d.snap.Lookup("system.model") == nil {
		t.Fatal("the seed folded patches from version 1 instead of reading the state at the cursor")
	}
}

// rowsThrough is a row log that carries bytes for each of the first n records.
func rowsThrough(t *testing.T, n uint64) store.Log[[]json.RawMessage] {
	t.Helper()
	rows := store.NewMemLog[[]json.RawMessage]()
	for lt := uint64(1); lt <= n; lt++ {
		if _, err := rows.Append(store.Entry[[]json.RawMessage]{FigaroLT: lt, Payload: []json.RawMessage{json.RawMessage(`{}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

type versionedForm struct{ patches []message.Patch }

func (v versionedForm) PatchesBetween(after, upTo uint64) []message.Patch {
	var out []message.Patch
	for i := after; i < upTo && int(i) < len(v.patches); i++ {
		out = append(out, v.patches[i])
	}
	return out
}

func setKey(key, value string) message.Patch {
	return form.Build(form.Snapshot{}, map[string]json.RawMessage{key: json.RawMessage(`"` + value + `"`)}, nil)
}

func midTurnLog(t *testing.T) store.Log[message.Message] {
	t.Helper()
	log := store.NewMemLog[message.Message]()
	for _, e := range []store.Entry[message.Message]{
		{FormChannelVersion: 1, Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("go")}}},
		{FormChannelVersion: 2, Payload: message.Message{Role: message.RoleOutput, StopReason: message.StopToolInvoke,
			Content: []message.Content{{Type: message.ContentToolInvoke, ToolCallID: "c1", ToolName: "bash"}}}},
		{FormChannelVersion: 2, Payload: message.Message{Role: message.RoleInput,
			Content: []message.Content{message.ToolResultContent("c1", "bash", "ok", false)}}},
	} {
		if _, err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

// A set that lands while the model streams is stamped on the assistant's
// entry, which can carry no reminder; it must ride the next user message, or
// the model never hears of it.
func TestAFormChangeDuringAStreamRidesTheNextUserMessage(t *testing.T) {
	bound := versionedForm{patches: []message.Patch{setKey("mantra", "first"), setKey("mantra", "second")}}
	log := midTurnLog(t)
	d := NewDeriver(bound, nil)
	entries, _ := store.TailAfter(log, 0)
	var got [][]message.Patch
	for _, e := range entries {
		msg, _, ok := d.Next(e)
		if !ok {
			continue
		}
		d.Commit(e, msg)
		got = append(got, msg.Patches)
	}
	if len(got[1]) != 0 {
		t.Fatalf("the assistant's entry took %d patch(es)", len(got[1]))
	}
	if len(got[2]) != 1 || (form.Snapshot{}).Apply(got[2][0]).Lookup("mantra") == nil {
		t.Fatalf("the tool results carry %v, want the change made during the stream", got[2])
	}
}

// A seed at an assistant's entry must start from the last user message's
// cursor, or a change made during that stream is skipped on resume.
func TestASeedAtAnAssistantEntryKeepsItsStreamChange(t *testing.T) {
	bound := versionedForm{patches: []message.Patch{setKey("mantra", "first"), setKey("mantra", "second")}}
	log := midTurnLog(t)
	d := NewDeriver(bound, nil)
	d.SeedAt(log, rowsThrough(t, 2))
	if d.At() != 1 {
		t.Fatalf("seeded at form version %d, want 1 (the last user message's)", d.At())
	}
	e, _ := log.Lookup(3)
	msg, _, _ := d.Next(e)
	if len(msg.Patches) != 1 {
		t.Fatalf("the tool results after a resume carry %d patch(es), want the stream's change", len(msg.Patches))
	}
}
