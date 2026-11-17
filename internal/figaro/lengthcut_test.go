package figaro_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/tool"
)

type lengthProvider struct {
	mu       sync.Mutex
	cuts     int
	calls    int
	withCall bool
	cutOn    map[int]bool
	seen     [][]message.Message
}

func (*lengthProvider) Name() string                                         { return "length-test" }
func (*lengthProvider) Fingerprint() string                                  { return "length-test/v1" }
func (*lengthProvider) SetModel(string)                                      {}
func (*lengthProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (p *lengthProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	var history []message.Message
	for _, e := range in.FigLog.ReadFrom(0, 1<<20) {
		history = append(history, e.Payload)
	}
	p.seen = append(p.seen, history)
	cut := call <= p.cuts || p.cutOn[call]
	p.mu.Unlock()
	if !cut {
		bus.PushDelta(message.TextContent("whole reply"))
		bus.PushFigaro(message.Message{
			Role: message.RoleOutput, StopReason: message.StopEnd,
			Content: []message.Content{message.TextContent("whole reply")}, Timestamp: time.Now().UnixMilli(),
		})
		return nil
	}
	content := []message.Content{message.TextContent("planning the page")}
	bus.PushDelta(message.TextContent("planning the page"))
	if p.withCall {
		done := message.Content{Type: message.ContentToolInvoke, ToolCallID: "write-0", ToolName: "write", Arguments: map[string]any{"path": "a"}}
		bus.PushToolInvokeStart("write-0", "write")
		bus.PushToolReady(done)
		content = append(content, done)
	}
	bus.PushToolInvokeStart("write-1", "write")
	bus.PushToolInvokeDelta("write-1", `{"path":"page.html","content":"<html><body><canvas`)
	bus.PushFigaro(message.Message{
		Role: message.RoleOutput, StopReason: message.StopLength, Usage: &message.Usage{OutputTokens: 16000},
		Content: content, Timestamp: time.Now().UnixMilli(),
	})
	return nil
}

func lengthNotice(msgs []message.Message) (message.Content, bool) {
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Sender == "angelus" && strings.Contains(c.Text, "max_tokens") {
				return c, true
			}
		}
	}
	return message.Content{}, false
}

// #80: a reply cut at max_tokens while writing a tool call ended the turn as if
// finished. The model must hear that its call was dropped and get one more go.
func TestALengthStopInsideAToolCallTellsTheModelAndContinues(t *testing.T) {
	prov := &lengthProvider{cuts: 1}
	a, reason := runCutTurnWith(t, prov, nil)

	assert.Equal(t, string(message.StopEnd), reason)
	assert.Equal(t, 2, prov.calls)
	notice, ok := lengthNotice(a.Context())
	require.True(t, ok, "the model must be told its call was cut")
	assert.Contains(t, notice.Text, "write")
	assert.Contains(t, notice.Text, "smaller")
	assert.Contains(t, notice.Text, "max_tokens=16000")
	assert.NotContains(t, notice.Text, "system.max_tokens", "an explicit max_tokens skips the provider's window clamp")
	resent := prov.seen[1]
	assert.Equal(t, notice.Text, resent[len(resent)-1].Content[0].Text)
}

func TestASecondLengthCutFailsTheTurnVisibly(t *testing.T) {
	prov := &lengthProvider{cuts: 5}
	_, reason := runCutTurnWith(t, prov, nil)

	assert.Equal(t, 2, prov.calls, "one continuation, no more")
	assert.True(t, strings.HasPrefix(reason, "error: "), reason)
	assert.Contains(t, reason, "max_tokens")
	assert.Contains(t, reason, "write")
}

func TestALengthCutBesideACompleteCallIsReportedWithTheResults(t *testing.T) {
	prov := &lengthProvider{cuts: 1, withCall: true}
	tools := tool.NewRegistry()
	counter := &countingTool{}
	tools.MustRegister(counter)
	a, reason := runCutTurnWith(t, prov, tools)

	assert.Equal(t, string(message.StopEnd), reason)
	assert.Equal(t, 2, prov.calls)
	assert.Equal(t, 1, counter.runs)
	_, ok := lengthNotice(a.Context())
	assert.True(t, ok, "the dropped second call must not vanish silently")
}

func TestACutBesideACompleteCallSpendsTheContinueBudget(t *testing.T) {
	prov := &lengthProvider{cuts: 5, withCall: true}
	tools := tool.NewRegistry()
	counter := &countingTool{}
	tools.MustRegister(counter)
	_, reason := runCutTurnWith(t, prov, tools)

	assert.Equal(t, 2, prov.calls, "one continuation, no more")
	assert.Equal(t, 2, counter.runs, "each round's complete call still runs")
	assert.True(t, strings.HasPrefix(reason, "error: "), reason)
	assert.Contains(t, reason, "write")
}

func TestEachTurnGetsItsOwnLengthCutContinue(t *testing.T) {
	prov := &lengthProvider{cutOn: map[int]bool{1: true, 3: true}}
	a, reason := runCutTurnWith(t, prov, nil)
	require.Equal(t, string(message.StopEnd), reason)

	ch, unsub := subscribeChan(a)
	defer unsub()
	submitPrompt(a, "again")
	reason = waitDoneReason(t, ch)

	assert.Equal(t, string(message.StopEnd), reason, "a cut in the first turn must not spend the second turn's continue")
	assert.Equal(t, 4, prov.calls)
}
