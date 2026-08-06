package anthropicsdk

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// The canary must fire on the shape that killed four turns in one day, and
// stay silent on a healthy stream — an instrument that cries every turn is
// noise, and one that never cries is decoration.
func TestWireCanary(t *testing.T) {
	owner := anthropic.ContentBlockUnion{Type: "tool_use", ID: "toolu_1", Name: "edit"}

	cases := []struct {
		name  string
		chunk string
		want  bool
	}{
		// The two bytes \t: correct, and what the SDK yields from a wire
		// carrying \\t. Nothing to report.
		{"escaped tab", `{"new_text": "\tif a {`, false},
		{"plain text", `{"path": "x.go"}`, false},
		// One RAW tab: the fragment can never reassemble into valid JSON.
		{"raw tab", "{\"new_text\": \"\tif a {", true},
		{"raw newline", "{\"new_text\": \"a\nb", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[int]bool{}
			reportUnescapedChunk(t.Context(), owner, 0, tc.chunk, `{"partial_json":"x"}`, seen)
			if seen[0] != tc.want {
				t.Fatalf("fired=%v want=%v for %q", seen[0], tc.want, tc.chunk)
			}
		})
	}

	// Once per block: fifty fragments of one edit are one event, not fifty.
	seen := map[int]bool{}
	for range 5 {
		reportUnescapedChunk(t.Context(), owner, 0, "{\"a\": \"\tb", `{}`, seen)
	}
	if !seen[0] {
		t.Fatal("the latch never set")
	}
}

// The verdict the canary reports is the one fact that assigns blame, so pin
// how it is read off the wire.
func TestWireCanaryVerdict(t *testing.T) {
	// A CORRECT wire double-escapes: the SSE line carries \\t.
	correct := `{"type":"content_block_delta","delta":{"partial_json":"{\"new_text\": \"\\tif a {"}}`
	if !strings.Contains(correct, `\\`) {
		t.Fatal("fixture: a correct wire carries a doubled escape")
	}
	// A SINGLE-ESCAPED wire does not — the fragment was decoded before it
	// reached us, and no downstream reader can undo that.
	single := `{"type":"content_block_delta","delta":{"partial_json":"{\"new_text\": \"\tif a {"}}`
	if strings.Contains(single, `\\`) {
		t.Fatal("fixture: the single-escaped form has no doubled escape")
	}
}
