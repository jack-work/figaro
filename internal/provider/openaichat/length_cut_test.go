package openaichat

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// #80: a call cut by finish_reason "length" ran with {} as its arguments.
func TestALengthStopDropsTheCallItCut(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_w","type":"function","function":{"name":"write","arguments":"{\"path\":\"page.html\",\"content\":\"<html"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	bus := &recordingBus{}
	got, err := drainSSE(context.Background(), strings.NewReader(stream), bus)
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call_a" {
		t.Fatalf("tool calls = %+v, want only the closed call_a", got.ToolCalls)
	}
	if len(bus.ready) != 1 || bus.ready[0].ToolCallID != "call_a" {
		t.Errorf("readied = %+v, want only call_a", bus.ready)
	}
	msg := got.toIRMessage()
	if msg.StopReason != message.StopLength {
		t.Errorf("stop = %q", msg.StopReason)
	}
}

func TestACallWithBrokenArgumentsAtANormalStopStillRuns(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write","arguments":"{\"path\""}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want the call kept so the tool's error reaches the model", got.ToolCalls)
	}
}
