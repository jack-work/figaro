package anthropicsdk

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/provider"
)

// TestEagerToolStreamingIsFormGated pins the opt-in and, more
// importantly, the refusal path.
//
// Anthropic buffers each tool parameter value until it is complete by default,
// which is why a large write argument arrives in one lump at the end.
// eager_input_streaming turns that off per tool — but the Copilot
// Anthropic-dialect endpoint rejects the field with a 400, so a provider that
// sets NoEagerToolStreaming must win over any form value.
func TestEagerToolStreamingIsFormGated(t *testing.T) {
	tools := []provider.Tool{{Name: "write", Description: "d", Parameters: map[string]any{}}}
	eagerOf := func(params anthropic.MessageNewParams) *bool {
		if len(params.Tools) == 0 || params.Tools[0].OfTool == nil {
			t.Fatal("no tool in params")
		}
		opt := params.Tools[0].OfTool.EagerInputStreaming
		if !opt.Valid() {
			return nil
		}
		v := opt.Value
		return &v
	}
	cases := []struct {
		name         string
		chalk        string
		eagerAllowed bool
		want         *bool // nil = field omitted
	}{
		{"absent leaves the API default", "", true, nil},
		{"false leaves the API default", `false`, true, nil},
		{"true opts in", `true`, true, boolPtr(true)},
		{"provider refusal beats the form", `true`, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := form.Snapshot{}
			if tc.chalk != "" {
				snap = form.FromMap(map[string]json.RawMessage{
					"system.eager_tool_streaming": json.RawMessage(tc.chalk),
				})
			}
			got := eagerOf(buildParams(nil, nil, snap, tools, 1024, false, "claude-opus-5", tc.eagerAllowed))
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("eager_input_streaming = %v, want omitted", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("eager_input_streaming omitted, want %v", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("eager_input_streaming = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
