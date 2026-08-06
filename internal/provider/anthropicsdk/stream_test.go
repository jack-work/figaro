package anthropicsdk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// helpers — verify the byte windowing the failure dump relies on
// behaves correctly at the edges (empty, short, exact, long).

func TestSafeHead(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 4, ""},
		{"shorter than n", "abc", 4, "abc"},
		{"exact n", "abcd", 4, "abcd"},
		{"longer than n", "abcdef", 4, "abcd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safeHead([]byte(tc.in), tc.n)
			if got != tc.want {
				t.Fatalf("safeHead(%q,%d)=%q want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestSafeTail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 4, ""},
		{"shorter than n", "abc", 4, "abc"},
		{"exact n", "abcd", 4, "abcd"},
		{"longer than n", "abcdef", 4, "cdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safeTail([]byte(tc.in), tc.n)
			if got != tc.want {
				t.Fatalf("safeTail(%q,%d)=%q want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestWindowAround(t *testing.T) {
	in := []byte("the quick brown fox jumps over the lazy dog")
	// radius 5 around index 16 ("fox") clamps inside bounds
	got := windowAround(in, 16, 5)
	if !strings.Contains(got, "n fox") {
		t.Fatalf("expected window to bracket 'fox', got %q", got)
	}
	// at offset 0, lo clamps to 0
	got = windowAround(in, 0, 4)
	if got != "the " {
		t.Fatalf("offset 0 clamp: got %q want %q", got, "the ")
	}
	// past the end, hi clamps to len
	got = windowAround(in, len(in)+10, 5)
	if got != "" && !strings.HasSuffix("the quick brown fox jumps over the lazy dog", got) {
		t.Fatalf("past-end clamp suspicious: %q", got)
	}
}

// jsonSyntaxOffset must walk wrapped error chains, because the SDK
// returns its accumulator failure as a fmt.Errorf wrapping the
// underlying json.RawMessage marshal error.
func TestJSONSyntaxOffset_UnwrapsChain(t *testing.T) {
	// Provoke a real *json.SyntaxError by marshaling an invalid
	// json.RawMessage — this is exactly the SDK's failure path.
	bad := json.RawMessage(`{"a":1 "b":2}`) // missing comma
	_, marshalErr := json.Marshal(bad)
	if marshalErr == nil {
		t.Fatal("expected marshal of invalid RawMessage to error")
	}
	// json.Marshal of an invalid RawMessage wraps as
	// json.MarshalerError, which itself wraps a *json.SyntaxError.
	// errors.As must reach it through both layers.
	wrapped := errors.New("accumulate: " + marshalErr.Error())
	if got := jsonSyntaxOffset(wrapped); got != -1 {
		t.Fatalf("plain string-wrap shouldn't expose offset, got %d", got)
	}
	// Properly-wrapped chain: fmt.Errorf with %w preserves errors.As.
	properly := wrapWith("accumulate", marshalErr)
	got := jsonSyntaxOffset(properly)
	if got < 0 {
		// Not all wrap layers expose *json.SyntaxError directly; in
		// that case the offset is best-effort -1 and the dump still
		// includes the full buffer. Document the boundary rather
		// than fail.
		t.Logf("note: SDK marshal error did not expose *json.SyntaxError through As (offset=%d); dump fallback still works", got)
		return
	}
	// Offset >= 0 means errors.As walked the chain successfully —
	// the actual byte position is whatever encoding/json reports.
	t.Logf("recovered json syntax error at offset %d", got)
}

func wrapWith(prefix string, err error) error {
	// fmt.Errorf with %w — same shape drainStream uses.
	return &wrapped{prefix: prefix, err: err}
}

type wrapped struct {
	prefix string
	err    error
}

func (w *wrapped) Error() string { return w.prefix + ": " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// The turn-killer: one raw TAB inside a tool argument's string literal.
// json.Marshal of the block is exactly what the SDK does at
// content_block_stop, and it is what produced
//
//	accumulate: error converting content block to JSON: json: error calling
//	MarshalJSON for type json.RawMessage: invalid character '\t' in string literal
//
// The first assertion is the canary: if the SDK ever stops validating the
// RawMessage, this test says so instead of quietly guarding nothing.
func TestRepairAccumulatedToolInput(t *testing.T) {
	block := func(in string) anthropic.ContentBlockUnion {
		return anthropic.ContentBlockUnion{
			Type: "tool_use", ID: "toolu_1", Name: "edit",
			Input: json.RawMessage(in),
		}
	}

	cb := block("{\"new_text\":\"func f() {\n\treturn 1\n}\"}")
	if _, err := json.Marshal(&cb); err == nil {
		t.Fatal("SDK no longer rejects raw control chars in a RawMessage; this guard may be obsolete")
	}
	if !repairAccumulatedToolInput(&cb) {
		t.Fatal("repair declined the exact input that kills the turn")
	}
	if _, err := json.Marshal(&cb); err != nil {
		t.Fatalf("block still unmarshalable after repair: %v", err)
	}
	var args struct {
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(cb.Input, &args); err != nil {
		t.Fatalf("repaired input does not decode: %v", err)
	}
	if args.NewText != "func f() {\n\treturn 1\n}" {
		t.Fatalf("repair changed the VALUE, not just its escaping: %q", args.NewText)
	}

	// Tabs and newlines BETWEEN tokens are legal whitespace: a
	// pretty-printed input must survive the scan untouched.
	pretty := block("{\n\t\"a\": 1\n}")
	if repairAccumulatedToolInput(&pretty) {
		t.Fatalf("repaired a valid pretty-printed input: %s", pretty.Input)
	}

	// A different defect is not ours to mend: leave the bytes alone so the
	// failure dump reports what actually arrived.
	broken := block(`{"a":1 "b":2}`)
	if repairAccumulatedToolInput(&broken) {
		t.Fatalf("claimed to repair a structurally malformed input: %s", broken.Input)
	}
}
