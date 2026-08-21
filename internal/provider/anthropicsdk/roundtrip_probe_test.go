package anthropicsdk

import (
	"encoding/json"
	"runtime/debug"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// CAN A STORED ROW BECOME A TYPED MessageParam AGAIN WITHOUT LOSS?
//
// The SDK provider hands typed values to a vendor client that owns
// marshalling, so a row read back from the log must reconstitute EXACTLY --
// not merely parse. This marshals a message carrying every block type this
// provider stores, unmarshals it, and marshals again: BYTE EQUALITY IS THE
// BAR, because a difference that survives a round trip is a difference the
// model would see.
func TestMessageParamRoundTripsThroughJSONWithoutLoss(t *testing.T) {
	cases := []struct {
		name string
		msg  anthropic.MessageParam
	}{
		{"text", anthropic.NewUserMessage(anthropic.NewTextBlock("hello"))},
		{"thinking with a signature", anthropic.NewAssistantMessage(
			anthropic.NewThinkingBlock("sig-abc123", "let me consider"),
			anthropic.NewTextBlock("the answer"),
		)},
		{"redacted thinking", anthropic.NewAssistantMessage(
			anthropic.NewRedactedThinkingBlock("opaque-payload"),
		)},
		{"tool use", anthropic.NewAssistantMessage(
			anthropic.NewToolUseBlock("toolu_1", map[string]any{"path": "x.go"}, "edit"),
		)},
		{"tool result", anthropic.NewUserMessage(
			anthropic.NewToolResultBlock("toolu_1", "output text", false),
		)},
		{"mixed", anthropic.NewAssistantMessage(
			anthropic.NewThinkingBlock("sig-2", "first"),
			anthropic.NewTextBlock("then"),
			anthropic.NewToolUseBlock("toolu_2", map[string]any{"a": 1, "b": "two"}, "run"),
		)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back anthropic.MessageParam
			if err := json.Unmarshal(first, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			second, err := json.Marshal(back)
			if err != nil {
				t.Fatalf("remarshal: %v", err)
			}
			if string(first) != string(second) {
				t.Fatalf("the round trip is LOSSY\n first: %s\nsecond: %s", first, second)
			}
		})
	}
}

// THE SDK'S VERSION IS READABLE AT RUNTIME, which is what lets the fingerprint
// carry it: a dependency bump invalidates the stored rows through the
// mechanism that already exists, with nothing new stored anywhere.
//
// A TEST BINARY CANNOT ASSERT THIS. debug.ReadBuildInfo().Deps is EMPTY under
// `go test` (measured: deps=0) and populated in a real binary (measured:
// 13 deps, anthropic-sdk-go v1.42.0). So this test states the fallback that
// production does not take, and says why rather than passing quietly.
func TestTheSDKVersionIsReadableFromBuildInfo(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok || len(info.Deps) == 0 {
		if got := sdkVersion(); got != "unknown" {
			t.Fatalf("no deps in this binary, so sdkVersion should be the fallback, got %q", got)
		}
		t.Skip("go test binaries carry no module deps; the real binary does")
	}
	if got := sdkVersion(); got == "unknown" || got == "" {
		t.Fatalf("deps are present but the SDK version read as %q", got)
	}
}
