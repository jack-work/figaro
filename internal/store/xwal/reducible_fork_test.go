package xwal

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// A reducible channel's fork state is derived from the SEGMENT holding the
// fork point: that segment's header (its watermark) folded with the patches
// after it, up to the point. disk.stateAtLocked does exactly that, and it
// recurses into l.parent when the index falls below the fork base.
//
// Flattening rests on that recursion following an injected parent rather than
// a directory ancestor. This pins the semantics it has to preserve: a child
// inherits everything through the fork point and nothing after it, without
// replaying the channel.
func TestReducibleForkInheritsThroughTheFork(t *testing.T) {
	f, trunk := seedTrunk(t, filepath.Join(t.TempDir(), "f"))

	set := func(k, v string) uint64 {
		_, mainLT, err := f.Append(trunk, 0, []byte(`"turn"`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.AppendChannel(trunk, "chalkboard", mainLT,
			[]byte(`{"set":{"`+k+`":"`+v+`"}}`), nil); err != nil {
			t.Fatal(err)
		}
		return mainLT
	}
	set("a", "1")
	forkAt := set("b", "2")
	set("c", "3") // after the fork point; the child must not see it

	alt, err := f.ForkAt(trunk, forkAt)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	state := chalkStateOf(t, f, alt)
	if _, ok := state["a"]; !ok {
		t.Fatalf("child lost the inherited prefix: %v", state)
	}
	if _, ok := state["b"]; !ok {
		t.Fatalf("child lost the fork-point patch: %v", state)
	}
	if _, ok := state["c"]; ok {
		t.Fatalf("child inherited past the fork point: %v", state)
	}

	// The parent keeps its own view, including what came after.
	parent := chalkStateOf(t, f, trunk)
	if _, ok := parent["c"]; !ok {
		t.Fatalf("fork disturbed the parent's state: %v", parent)
	}
}

func chalkStateOf(t *testing.T, f *Trunks, trunk string) map[string]any {
	t.Helper()
	head, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := head.StateAt("chalkboard", channelTail(head, "chalkboard"))
	head.Close()
	if err != nil {
		t.Fatalf("StateAt(%s): %v", trunk, err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("state %q: %v", raw, err)
	}
	return got
}
