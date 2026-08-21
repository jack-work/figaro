package store

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

// THE ENTRY AN APPEND RETURNS CARRIES THE SAME STAMP THE LOG DOES.
//
// FormChannelVersion and StudyVersions are stamped BY the append -- the
// caller cannot compute them -- and xwalLog.Append recovers them by READING
// THE RECORD BACK. This test pins the equivalence rather than the mechanism:
// whatever the append returns must equal what the log serves for that LT.
//
// It is the guard for hoisting the stamp out of the readback. If the returned
// value is ever derived a second way, this is what goes red.
func TestTheAppendedEntryCarriesTheSameStampTheLogServes(t *testing.T) {
	be, aria := NewTestAria(t, "d", message.Patch{})
	ir, err := be.OpenFigIR(aria)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"one", "two", "three"} {
		if _, err := be.ApplyForm(aria, message.Patch{Set: map[string]json.RawMessage{
			"system.credo": json.RawMessage(`"v` + string(rune('0'+i)) + `"`),
		}}); err != nil {
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
