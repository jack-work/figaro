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

func TestReplayMasksDefaultOn(t *testing.T) {
	all := ReplayMasks{Notice: true, Clock: true, Identity: true, Tools: true}
	if got := ReadReplayMasks(Snapshot{}); got != all {
		t.Fatalf("no keys: %+v", got)
	}
	s := FromMap(map[string]json.RawMessage{ReplayKey: json.RawMessage(`{"of":"abc","turn":2,"clock":false,"tools":false}`)})
	if got := ReadReplayMasks(s); got != (ReplayMasks{Notice: true}) {
		t.Fatalf("clock and tools off: %+v", got)
	}
}

// Defends #114: a tool that runs for real acts as the figaro that runs it. With
// the original's id in view, the model would aim figaro verbs at the original.
func TestRealToolsUnmaskTheIdentity(t *testing.T) {
	s := FromMap(map[string]json.RawMessage{ReplayKey: json.RawMessage(`{"of":"abc","turn":2,"identity":true,"tools":false}`)})
	if got := ReadReplayMasks(s); got.Identity || got.Tools {
		t.Fatalf("identity held over real tools: %+v", got)
	}
}
