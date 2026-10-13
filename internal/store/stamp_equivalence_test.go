package store

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// The entry an append returns carries the same stamp the log serves for that
// LT: FormChannelVersion and StudyVersions are stamped by the append, and
// this pins the equivalence rather than the mechanism.
func TestTheAppendedEntryCarriesTheSameStampTheLogServes(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	ir, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"one", "two", "three"} {
		if _, err := be.ApplyForm(aria, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.credo": json.RawMessage(`"v` + string(rune('0'+i)) + `"`),
		}, nil)); err != nil {
			t.Fatal(err)
		}
		returned, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent(text)},
		}})
		if err != nil {
			t.Fatal(err)
		}

		served, ok := ir.Lookup(returned.LT)
		if !ok {
			t.Fatalf("record %d: nothing at LT %d", i, returned.LT)
		}
		if returned.FormChannelVersion != served.FormChannelVersion {
			t.Fatalf("record %d: append returned FormChannelVersion=%d, the log serves %d",
				i, returned.FormChannelVersion, served.FormChannelVersion)
		}
		if len(returned.StudyVersions) != len(served.StudyVersions) {
			t.Fatalf("record %d: append returned %d study versions, the log serves %d",
				i, len(returned.StudyVersions), len(served.StudyVersions))
		}
		for fid, v := range served.StudyVersions {
			if returned.StudyVersions[fid] != v {
				t.Fatalf("record %d: study %s returned %d, served %d",
					i, fid, returned.StudyVersions[fid], v)
			}
		}
		if returned.FormChannelVersion == 0 {
			t.Fatalf("record %d: the stamp is zero, so this test would pass on a "+
				"build that stamped nothing", i)
		}
	}
}
