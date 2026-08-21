package openaichat

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The streamed body must be the SAME BYTES the marshalled one is. This tenant
// has its own oracle because it has its own request type: the splice is shared
// and the field order is not.
func TestStreamedBodyIsByteIdenticalToMarshal(t *testing.T) {
	rows := func(bodies ...string) []json.RawMessage {
		out := make([]json.RawMessage, 0, len(bodies))
		for _, b := range bodies {
			out = append(out, json.RawMessage(b))
		}
		return out
	}
	cases := []struct {
		name string
		req  chatRequest
	}{
		{"no messages", chatRequest{Model: "m"}},
		{"html escapable", chatRequest{Model: "m", Messages: rows(
			`{"role":"user","content":"a < b && c > d"}`,
			"{\n  \"role\": \"assistant\"\n}",
		)}},
		{"fields on both sides of the splice", chatRequest{
			Model:          "m",
			Messages:       rows(`{"role":"user"}`),
			Tools:          []chatTool{{Type: "function"}},
			Stream:         true,
			MaxTokens:      64,
			SessionID:      "s",
			PromptCacheKey: "s",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got bytes.Buffer
			if err := bodyFunc(tc.req)(&got); err != nil {
				t.Fatalf("stream: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("streamed body differs\n got: %s\nwant: %s", got.Bytes(), want)
			}
		})
	}
}
