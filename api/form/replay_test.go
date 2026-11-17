package form

import (
	"encoding/json"
	"testing"
)

func TestReadReplayWantsATrunkAndATurn(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"of":"abc","turn":2,"lt":9}`: true,
		`{"of":"abc","turn":2}`:        true,
		`{"of":"abc"}`:                 false,
		`{"turn":2}`:                   false,
		`"abc:2"`:                      false,
	} {
		s := FromMap(map[string]json.RawMessage{ReplayKey: json.RawMessage(raw)})
		if _, ok := ReadReplay(s); ok != want {
			t.Errorf("%s: ok=%v, want %v", raw, ok, want)
		}
	}
	r := Replay{Of: "abc", Turn: 2, LT: 9}
	got, ok := ReadReplay(Snapshot{}.Apply(r.Patch(Snapshot{})))
	if !ok || got != r {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
}
