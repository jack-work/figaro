package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// Folding every patch the figaro was shown reproduces its form exactly. A
// patch is shown when some IR entry's cursor stamp is at or above its
// version; a patch nobody's stamp reaches is state the aria has and was
// never told about. The outfit needs no special case: the null root's form
// is empty, so the outfit's own patch is the snapshot.
func TestEveryPatchIsShownToTheAria(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	outfit, err := be.CreateOutfit("opus5", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.golang": json.RawMessage(`{"frontmatter":"name: golang"}`),
		"duke-title":    json.RawMessage(`"Gluck"`),
	}, nil))
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
		if _, err := be.ApplyForm(aria, form.Build(form.Snapshot{}, kv, nil)); err != nil {
			t.Fatal(err)
		}
	}
	lg, err := be.OpenFigIR(aria)
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

	patches, err := be.FormPatchesBetween(aria, 0, ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	// The projection's walk: one forward cursor, entries in order.
	shown := form.Snapshot{}
	cursor := 0
	for _, e := range lg.ReadFrom(0, 0) {
		if e.Payload.Role == message.RoleGenesis {
			continue
		}
		for cursor < len(patches) && patches[cursor].Version <= e.FormChannelVersion {
			shown = shown.Apply(patches[cursor].Patch)
			cursor++
		}
	}
	if cursor < len(patches) {
		var missed []string
		for _, p := range patches[cursor:] {
			for _, ent := range p.Patch.Entries() {
				if ent.IsRemoval() {
					continue
				}
				k := ent.Key
				missed = append(missed, fmt.Sprintf("%s (v%d)", k, p.Version))
			}
		}
		sort.Strings(missed)
		t.Errorf("%d patch(es) were never shown to the aria: %v", len(patches)-cursor, missed)
	}

	state, err := be.FormState(aria)
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

// A fork changes the aria's identity, and the branch must be told: the new
// aria_id is an ordinary form patch, so the child sees the old value first
// and then the transition.
func TestAForkedAriaIsShownItsNewAriaID(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	outfit, err := be.CreateOutfit("opus5", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"skills.golang": json.RawMessage(`{"frontmatter":"name: golang"}`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	setID := func(aria string) {
		t.Helper()
		if _, err := be.ApplyForm(aria, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"aria_id": json.RawMessage(`"` + aria + `"`),
		}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	say := func(aria, text string) {
		t.Helper()
		lg, err := be.OpenFigIR(aria)
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

	patches, err := be.FormPatchesBetween(alt, 0, ^uint64(0))
	if err != nil {
		t.Fatal(err)
	}
	lg, err := be.OpenFigIR(alt)
	if err != nil {
		t.Fatal(err)
	}
	// The projection's walk, collecting the aria_id values in the order the
	// branch is told about them.
	var seen []string
	shown := form.Snapshot{}
	cursor := 0
	for _, e := range lg.ReadFrom(0, 0) {
		if e.Payload.Role == message.RoleGenesis {
			continue
		}
		for cursor < len(patches) && patches[cursor].Version <= e.FormChannelVersion {
			p := patches[cursor]
			if ent, ok := p.Patch.Entry("aria_id"); ok {
				seen = append(seen, string(ent.New))
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
