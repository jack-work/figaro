package figaro

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// A drained batch is one message at one LT, separated by blank lines: a lone
// newline is a soft break in markdown. Logs written before this read unchanged.
func TestMergePromptEvents_JoinsWithABlankLine(t *testing.T) {
	got, ok := mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "a"},
		{typ: eventUserPrompt, text: "b"},
		{typ: eventUserPrompt, text: "c"},
	})
	if !ok {
		t.Fatal("a non-empty batch must merge")
	}
	if got.text != "a\n\nb\n\nc" {
		t.Errorf("text = %q, want %q: blank line between, no trim", got.text, "a\n\nb\n\nc")
	}
}

// A single prompt passes through untouched.
func TestMergePromptEvents_EmptyAndSingle(t *testing.T) {
	if _, ok := mergePromptEvents(nil); ok {
		t.Error("an empty batch must not produce a message")
	}
	one := event{typ: eventUserPrompt, text: "solo", form: &rpc.FormInput{}}
	got, ok := mergePromptEvents([]event{one})
	if !ok || got.text != "solo" || got.form != one.form {
		t.Errorf("a single prompt must pass through unchanged, got %+v", got)
	}
}

// A blank prompt contributes no line.
func TestMergePromptEvents_SkipsEmptyTexts(t *testing.T) {
	got, _ := mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "first"},
		{typ: eventUserPrompt, text: ""},
		{typ: eventUserPrompt, text: "second"},
	})
	if got.text != "first\n\nsecond" {
		t.Errorf("text = %q, want %q", got.text, "first\n\nsecond")
	}
}

// Merging N events merges N form patches in queue order; a later value wins.
func TestMergePromptEvents_MergesFormInQueueOrder(t *testing.T) {
	got, _ := mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "a", form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"keep": json.RawMessage(`1`)},
			Patch:   ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"model": json.RawMessage(`"old"`)}, []string{"x"})),
		}},
		{typ: eventUserPrompt, text: "b", form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"also": json.RawMessage(`2`)},
			Patch:   ptrPatch(form.Build(form.Snapshot{}, map[string]json.RawMessage{"model": json.RawMessage(`"new"`)}, []string{"y"})),
		}},
	})
	cb := got.form
	if cb == nil {
		t.Fatal("form input was dropped entirely")
	}
	if string(cb.Context["keep"]) != "1" || string(cb.Context["also"]) != "2" {
		t.Errorf("context lost a contributor: %v", cb.Context)
	}
	if string(mustEntry(*cb.Patch, "model")) != `"new"` {
		t.Errorf("later value must win: model = %s", mustEntry(*cb.Patch, "model"))
	}
	removed := 0
	for _, e := range cb.Patch.Entries() {
		if e.IsRemoval() {
			removed++
		}
	}
	if removed != 2 {
		t.Errorf("removals must accumulate, got %d: %v", removed, cb.Patch.Entries())
	}
}

// Merging must not mutate the inputs: the batch is prepended back on failure.
func TestMergePromptEvents_DoesNotMutateInputs(t *testing.T) {
	a := &rpc.FormInput{Patch: ptrPatch(form.Build(form.Snapshot{},
		map[string]json.RawMessage{"k": json.RawMessage(`"a"`)}, nil))}
	b := &rpc.FormInput{Patch: ptrPatch(form.Build(form.Snapshot{},
		map[string]json.RawMessage{"k": json.RawMessage(`"b"`)}, nil))}
	mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "x", form: a},
		{typ: eventUserPrompt, text: "y", form: b},
	})
	ae, _ := a.Patch.Entry("k")
	be, _ := b.Patch.Entry("k")
	if string(ae.New) != `"a"` || string(be.New) != `"b"` {
		t.Error("merge mutated a caller's form input")
	}
}
