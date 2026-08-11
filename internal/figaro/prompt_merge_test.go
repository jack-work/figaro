package figaro

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
)

// A drained batch is ONE message. Three nudges typed during one tool round are
// three lines at one LT, not three messages the model must reconcile.
//
// Backcompat is read-only and lives elsewhere: logs written before this carry N
// separate user messages and keep reading exactly as they did, because nothing
// on disk is migrated and turns.Opens / turns.IsSteering / compose.Turns are
// unchanged. This test pins the WRITE side.
// The separator is a BLANK LINE, and both halves of that matter. On screen,
// prose is markdown: a lone newline is a SOFT break, so glamour rejoined three
// messages into one sentence ("test2 test3 test4"): which is how this was
// reported. For the model, a blank line is the unambiguous mark of separate
// messages. One separator satisfies both, so what the reader sees and what the
// agent reads cannot drift.
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

// An empty batch merges to nothing, and a single prompt is passed through
// untouched so the common case allocates nothing and cannot be reshaped.
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

// A blank prompt contributes no line: it must not leave a stray empty row in
// the middle of the merged text.
func TestMergePromptEvents_SkipsEmptyTexts(t *testing.T) {
	got, _ := mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "first"},
		{typ: eventUserPrompt, text: ""},
		{typ: eventUserPrompt, text: "second"},
	})
	// A BLANK line between them: a lone newline is a soft break in markdown,
	// so the screen rejoined the messages into one sentence.
	if got.text != "first\n\nsecond" {
		t.Errorf("text = %q, want %q", got.text, "first\n\nsecond")
	}
}

// PER-EVENT SIDE DATA MUST NOT BE LOST. Each queued prompt may carry form
// input; merging N events into one message must merge N patches, in queue order,
// so a later value wins and nothing is silently dropped.
func TestMergePromptEvents_MergesFormInQueueOrder(t *testing.T) {
	got, _ := mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "a", form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"keep": json.RawMessage(`1`)},
			Patch: &rpc.FormPatch{
				Set:    map[string]json.RawMessage{"model": json.RawMessage(`"old"`)},
				Remove: []string{"x"},
			},
		}},
		{typ: eventUserPrompt, text: "b", form: &rpc.FormInput{
			Context: map[string]json.RawMessage{"also": json.RawMessage(`2`)},
			Patch: &rpc.FormPatch{
				Set:    map[string]json.RawMessage{"model": json.RawMessage(`"new"`)},
				Remove: []string{"y"},
			},
		}},
	})
	cb := got.form
	if cb == nil {
		t.Fatal("form input was dropped entirely")
	}
	if string(cb.Context["keep"]) != "1" || string(cb.Context["also"]) != "2" {
		t.Errorf("context lost a contributor: %v", cb.Context)
	}
	if string(cb.Patch.Set["model"]) != `"new"` {
		t.Errorf("later value must win: model = %s", cb.Patch.Set["model"])
	}
	if len(cb.Patch.Remove) != 2 {
		t.Errorf("removals must accumulate, got %v", cb.Patch.Remove)
	}
}

// Merging must not mutate the inputs: the batch is prepended back to the inbox
// on failure, and a mutated event would be restored in the wrong shape.
func TestMergePromptEvents_DoesNotMutateInputs(t *testing.T) {
	a := &rpc.FormInput{Patch: &rpc.FormPatch{
		Set: map[string]json.RawMessage{"k": json.RawMessage(`"a"`)},
	}}
	b := &rpc.FormInput{Patch: &rpc.FormPatch{
		Set: map[string]json.RawMessage{"k": json.RawMessage(`"b"`)},
	}}
	mergePromptEvents([]event{
		{typ: eventUserPrompt, text: "x", form: a},
		{typ: eventUserPrompt, text: "y", form: b},
	})
	if string(a.Patch.Set["k"]) != `"a"` || string(b.Patch.Set["k"]) != `"b"` {
		t.Error("merge mutated a caller's form input")
	}
}
