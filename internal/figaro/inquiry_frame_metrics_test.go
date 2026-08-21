package figaro_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/message"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// TestInquiryFrameCarriesFreshMetrics pins the ORDER inside appendUserPrompt:
// the durable append, then refreshMetrics, then OpenInquiry.
//
// Every aria-server broadcast is stamped with a.sessionMetrics() by the
// subscription in NewAgent, so the frame that first carries the user's
// question also carries the status footer's numbers. Refreshing metrics
// BEFORE the broadcast is what makes that frame describe a world in which
// the question already exists: its context-token estimate is counted, and -
// on a first prompt, where appendUserPrompt seeds the mantra from the
// opening text: the mantra is populated.
//
// Broadcast-before-refresh would emit a first frame whose Mantra is "" and
// whose ContextTokens is 0: the footer would name no conversation on the
// very frame that introduces it, until some later frame corrected it. The
// measured cost of that ordering is ~0.6µs (BenchmarkPromptBroadcastGap,
// flat from 100 to 5,000 messages), so the trade is not worth taking.
func TestInquiryFrameCarriesFreshMetrics(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Patch{Set: map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"idle-test"`),
	}})
	testBE, testID := store.NewTestAria(t, "d", message.Patch{})
	a := figaro.NewAgent(figaro.Config{
		Backend:    testBE,
		Projector:  uiir.New(nil),
		ID:         testID,
		SocketPath: "/tmp/inquiry-frame-metrics.sock",
		Provider:   &idleProvider{},
		Tools:      tool.NewRegistry(),
		Form:       cb,
	})
	defer a.Kill()

	frames, stop := subscribeChan(a)
	defer stop()

	const question = "who paints the question, and when?"
	submitPrompt(a, question)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no aria frame carried the inquiry")
		case n := <-frames:
			if n.Method != rpc.MethodAriaFrame {
				continue
			}
			page, ok := n.Params.(aria.Page)
			if !ok || len(page.Parts) == 0 || page.Parts[0].Turn.Inquiry != question {
				continue
			}
			require.NotNil(t, page.Metrics, "the inquiry frame must carry metrics")
			// The mantra is seeded from this very prompt; the frame that
			// announces the question must already know the aria's name.
			require.Equal(t, strings.ToValidUTF8(question, ""), page.Metrics.Mantra,
				"inquiry frame metrics are one message stale (mantra unset)")
			require.Greater(t, page.Metrics.ContextTokens, 0,
				"inquiry frame metrics are one message stale (question not counted)")
			return
		}
	}
}
