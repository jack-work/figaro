package anthropicsdk

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// BenchmarkParseRowsToMessageParams is the cost the CatchUp conversion would
// ADD to every send on this provider, and it is the reason the conversion is
// raised rather than made.
//
// The raw providers (anthropic, openaichat, copilot/responses) hand stored
// rows to the wire verbatim: their whole-history read is slice headers. This
// one hands anthropic.MessageParam VALUES to the vendor SDK, which owns
// marshalling the request -- so a read that keeps nothing must UNMARSHAL the
// entire conversation on every turn, where the deleted memo parsed each row
// once, when it first arrived.
func BenchmarkParseRowsToMessageParams(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 50_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			rows := make([]json.RawMessage, n)
			for i := range rows {
				body, err := json.Marshal(anthropic.MessageParam{
					Role: anthropic.MessageParamRoleUser,
					Content: []anthropic.ContentBlockParamUnion{
						anthropic.NewTextBlock("turn body " + strconv.Itoa(i) +
							" with enough text to be a plausible message on the wire"),
					},
				})
				if err != nil {
					b.Fatal(err)
				}
				rows[i] = body
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				msgs := make([]anthropic.MessageParam, 0, n)
				for _, raw := range rows {
					var msg anthropic.MessageParam
					if err := json.Unmarshal(raw, &msg); err != nil {
						b.Fatal(err)
					}
					msgs = append(msgs, msg)
				}
				if len(msgs) != n {
					b.Fatalf("got %d, want %d", len(msgs), n)
				}
			}
		})
	}
}
