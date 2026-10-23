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
	d.SeedAt(log, e.LT)
	if d.snap.Lookup("system.model") == nil {
		t.Fatal("the seed folded patches from version 1 instead of reading the state at the cursor")
	}
}
