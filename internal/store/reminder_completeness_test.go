package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
)

// THE INVARIANT: folding every patch the figaro was shown reproduces its
// chalkboard exactly.
//
// A patch is shown when some IR entry's cursor stamp is at or above the
// patch's version -- that is how the projection decides what to render as
// <system-reminder> blocks. So a patch nobody's stamp reaches is state the
// aria HAS and has never been told about, which is invisible: `figaro
// state` shows it, the model does not know it.
//
// Two defects hid under the absence of this assertion. The outfit patch
// was written after the record meant to introduce it, so no aria rendered
// its skills; and every entry appended in-process came back with cursor
// zero, because the cache stored the caller's struct instead of the record
// that was written -- so nothing rendered at all until a restart.
//
// The outfit needs no special case, and that is the point of the model:
// the null root's board is empty, so the outfit's own patch IS the
// snapshot, and every aria forked from the stump inherits it as an
// ordinary delta.
func TestEveryPatchIsShownToTheAria(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	outfit, err := be.CreateOutfit("opus5", message.Patch{Set: map[string]json.RawMessage{
		"skills.golang": json.RawMessage(`{"frontmatter":"name: golang"}`),
		"duke-title":    json.RawMessage(`"Gluck"`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	aria, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}

	// What the daemon does: identity keys, then a turn, then per-turn keys.
	apply := func(kv map[string]json.RawMessage) {
		t.Helper()
		if _, err := be.ApplyChalkboard(aria, message.Patch{Set: kv}); err != nil {
			t.Fatal(err)
		}
	}
	lg, err := be.Open(aria)
	if err != nil {
		t.Fatal(err)
	}
	say := func(text string) {
		t.Helper()
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{{Type: message.ContentProse, Text: text}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	apply(map[string]json.RawMessage{"aria_id": json.RawMessage(`"x"`)})
	apply(map[string]json.RawMessage{"cwd": json.RawMessage(`"/tmp"`), "datetime": json.RawMessage(`"now"`)})
	say("first")
	apply(map[string]json.RawMessage{"mantra": json.RawMessage(`"a mantra"`)})
	say("second")

	patches, err := be.ChalkboardPatches(aria)
	if err != nil {
		t.Fatal(err)
	}
	// The projection's walk: one forward cursor, entries in order.
	shown := chalkboard.Snapshot{}
	cursor := 0
	for _, e := range lg.ReadFrom(0, 0) {
		if e.Payload.Role == message.RoleGenesis {
			continue
		}
		for cursor < len(patches) && patches[cursor].Version <= e.ChalkVersion {
			shown = shown.Apply(patches[cursor].Patch)
			cursor++
		}
	}
	if cursor < len(patches) {
		var missed []string
		for _, p := range patches[cursor:] {
			for k := range p.Patch.Set {
				missed = append(missed, fmt.Sprintf("%s (v%d)", k, p.Version))
			}
		}
		sort.Strings(missed)
		t.Errorf("%d patch(es) were never shown to the aria: %v", len(patches)-cursor, missed)
	}

	state, err := be.ChalkboardState(aria)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range state.All() {
		got, ok := shown.Get(k)
		if !ok {
			t.Errorf("key %q is in the aria's state and was never shown to it", k)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("key %q: shown %s, state %s", k, got, want)
		}
	}
}

// A fork changes the aria's identity, and the branch must be TOLD -- the
// new aria_id is an ordinary chalkboard patch, so it has to reach the
// child as an ordinary delta. The child inherits its parent's history, so
// it sees the old value first and then the transition, which is the whole
// point of encoding state as point-in-time deltas: the model can read its
// own past and know which value is current.
func TestAForkedAriaIsShownItsNewAriaID(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	outfit, err := be.CreateOutfit("opus5", message.Patch{Set: map[string]json.RawMessage{
		"skills.golang": json.RawMessage(`{"frontmatter":"name: golang"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	setID := func(aria string) {
		t.Helper()
		if _, err := be.ApplyChalkboard(aria, message.Patch{Set: map[string]json.RawMessage{
			"aria_id": json.RawMessage(`"` + aria + `"`),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	say := func(aria, text string) {
		t.Helper()
		lg, err := be.Open(aria)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lg.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{{Type: message.ContentProse, Text: text}},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	setID(parent)
	say(parent, "on the parent")

	_, alt, err := be.Fork(parent)
	if err != nil {
		t.Fatal(err)
	}
	setID(alt) // what the daemon stamps on a fresh branch
	say(alt, "on the branch")

	patches, err := be.ChalkboardPatches(alt)
	if err != nil {
		t.Fatal(err)
	}
	lg, err := be.Open(alt)
	if err != nil {
		t.Fatal(err)
	}
	// The projection's walk, collecting the aria_id values in the order the
	// branch is told about them.
	var seen []string
	shown := chalkboard.Snapshot{}
	cursor := 0
	for _, e := range lg.ReadFrom(0, 0) {
		if e.Payload.Role == message.RoleGenesis {
			continue
		}
		for cursor < len(patches) && patches[cursor].Version <= e.ChalkVersion {
			p := patches[cursor]
			if v, ok := p.Patch.Set["aria_id"]; ok {
				seen = append(seen, string(v))
			}
			shown = shown.Apply(p.Patch)
			cursor++
		}
	}

	want := `"` + alt + `"`
	if len(seen) == 0 {
		t.Fatalf("the branch was never shown an aria_id at all (it has %d patches)", len(patches))
	}
	if seen[len(seen)-1] != want {
		t.Errorf("the branch was shown aria_id values %v; the last must be its own %s", seen, want)
	}
	if len(seen) < 2 {
		t.Errorf("the branch saw only %v: it should see the parent's id and then the TRANSITION to its own", seen)
	}
	if got, _ := shown.Get("aria_id"); string(got) != want {
		t.Errorf("folding what the branch was shown gives aria_id=%s, want %s", got, want)
	}
	// And the outfit still reaches the branch across the fork.
	if _, ok := shown.Get("skills.golang"); !ok {
		t.Error("the branch was never shown the outfit's skills")
	}
}
