package responses

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/provider"
)

var errSocketReset = errors.New("failed to get reader: received close frame: status = StatusAbnormalClosure")

func cutFrames(sse string, end error) responseFrames {
	next := sseFrames(strings.NewReader(sse))
	return func(ctx context.Context) ([]byte, error) {
		raw, err := next(ctx)
		if err == io.EOF && end != nil {
			return nil, end
		}
		return raw, err
	}
}

const responsesPrefix = "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"m1\",\"delta\":\"Now a test\"}\n\n" +
	"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc1\",\"call_id\":\"call_w\",\"name\":\"write\",\"arguments\":\"\"}}\n\n" +
	"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"item_id\":\"fc1\",\"delta\":\"{\\\"path\\\":\\\"test/pdf-pa\"}\n\n"

// A cut before any call is ready hands nothing over, so the turn can keep
// the prose and resend instead of committing half a reply.
func TestAResponsesStreamCutBeforeAnyCallIsReadyHandsNothingOver(t *testing.T) {
	bus := &captureBus{}
	got, err := readResponseStream(context.Background(), cutFrames(responsesPrefix, errSocketReset), bus)

	cut, ok := provider.AsStreamCut(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, 3, cut.Events)
	require.ErrorIs(t, err, errSocketReset)
	require.Empty(t, got.Output)
}

func TestAResponsesStreamThatEndsBeforeCompletedIsAStreamCut(t *testing.T) {
	_, err := readResponseStream(context.Background(), cutFrames(responsesPrefix, nil), &captureBus{})

	cut, ok := provider.AsStreamCut(err)
	require.True(t, ok, "got %v", err)
	require.NoError(t, cut.Err)
}

func TestAResponsesStreamCutAfterACallIsReadyKeepsItsOutput(t *testing.T) {
	sse := responsesPrefix +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"item_id\":\"fc1\",\"delta\":\"ges.py\\\"}\"}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc1\",\"call_id\":\"call_w\",\"name\":\"write\",\"arguments\":\"{\\\"path\\\":\\\"test/pdf-pages.py\\\"}\"}}\n\n"
	got, err := readResponseStream(context.Background(), cutFrames(sse, errSocketReset), &captureBus{})

	_, ok := provider.AsStreamCut(err)
	require.True(t, ok, "got %v", err)
	require.NotEmpty(t, got.Output, "a readied call may have run: its output must be handed over")
}

func TestACancelledResponsesStreamIsNotAStreamCut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readResponseStream(ctx, cutFrames(responsesPrefix, context.Canceled), &captureBus{})
	_, ok := provider.AsStreamCut(err)
	require.False(t, ok)
	require.ErrorIs(t, err, context.Canceled)
}
