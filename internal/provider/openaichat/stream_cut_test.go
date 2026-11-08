package openaichat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/provider"
)

var errPeerReset = errors.New("stream error: stream ID 41; INTERNAL_ERROR; received from peer")

type resetReader struct {
	r   io.Reader
	err error
}

func (b *resetReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

const chatPrefix = `data: {"choices":[{"delta":{"content":"Now a test"}}]}` + "\n\n" +
	`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_w","type":"function","function":{"name":"write","arguments":"{\"path\":\"test/pdf-pa"}}]}}]}` + "\n\n"

// A GPT route cut mid tool call used to hand over half a write as if it were
// whole, or fail with a bare read error.
func TestAChatStreamResetMidToolCallIsAStreamCut(t *testing.T) {
	bus := &recordingBus{}
	_, err := drainSSE(context.Background(), &resetReader{r: strings.NewReader(chatPrefix), err: errPeerReset}, bus)

	cut, ok := provider.AsStreamCut(err)
	if !ok {
		t.Fatalf("got %v, want a StreamCut", err)
	}
	if cut.Events != 2 || !errors.Is(err, errPeerReset) {
		t.Errorf("cut = %+v", cut)
	}
	if len(bus.ready) != 0 {
		t.Errorf("a cut call was readied: %+v", bus.ready)
	}
}

func TestAChatStreamThatStopsWithNoFinishReasonIsAStreamCut(t *testing.T) {
	bus := &recordingBus{}
	_, err := drainSSE(context.Background(), strings.NewReader(chatPrefix), bus)

	cut, ok := provider.AsStreamCut(err)
	if !ok {
		t.Fatalf("got %v, want a StreamCut", err)
	}
	if cut.Err != nil {
		t.Errorf("a clean early close carries no cause, got %v", cut.Err)
	}
	if len(bus.ready) != 0 {
		t.Errorf("a cut call was readied: %+v", bus.ready)
	}
}

func TestAChatStreamWithAFinishReasonAndNoDoneIsWhole(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"content":"done"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if got.Text != "done" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestAnEmptyChatStreamIsNotAStreamCut(t *testing.T) {
	_, err := drainSSE(context.Background(), strings.NewReader(""), &recordingBus{})
	if _, ok := provider.AsStreamCut(err); ok {
		t.Errorf("an empty body is not a cut stream: %v", err)
	}
}
