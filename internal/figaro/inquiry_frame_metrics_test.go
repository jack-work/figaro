package figaro_test

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/message"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/uiir"
)

// appendUserPrompt refreshes metrics before OpenInquiry, so the frame that
// introduces the question carries its context count and the seeded mantra.
func TestInquiryFrameCarriesFreshMetrics(t *testing.T) {
	cb, _ := form.Open("")
	cb.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.model":    json.RawMessage(`"mock-model-v1"`),
		"system.provider": json.RawMessage(`"idle-test"`),
	}, nil))
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
			require.Equal(t, strings.ToValidUTF8(question, ""), page.Metrics.Mantra,
				"inquiry frame metrics are one message stale (mantra unset)")
			require.Greater(t, page.Metrics.ContextTokens, 0,
				"inquiry frame metrics are one message stale (question not counted)")
			return
		}
	}
}
