package anthropic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/provider"
)

const ssePrefix = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Now a test"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"write","input":{}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"test/pdf-pa"}}` + "\n\n"

const sseStop = "event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":1}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type cutBody struct {
	r   io.Reader
	err error
}

func (b *cutBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF && b.err != nil {
		return n, b.err
	}
	return n, err
}

func (*cutBody) Close() error { return nil }

var errPeerReset = errors.New("stream error: stream ID 697; CANCEL; received from peer")

func TestAStreamResetMidToolUseIsAStreamCutNamingTheCause(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag"}
	_, err := a.drainSSE(context.Background(), &cutBody{r: strings.NewReader(ssePrefix), err: errPeerReset}, "claude-test", noOpBus{})
	err = provider.NameStreamCut(err, "copilot")

	cut, ok := provider.AsStreamCut(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 6, cut.Events)
	assert.ErrorIs(t, err, errPeerReset)
	assert.Equal(t, "copilot: the upstream cut the stream after 6 events, before message_stop (stream error: stream ID 697; CANCEL; received from peer)", err.Error())
}

func TestAStreamThatEndsCleanBeforeMessageStopIsAStreamCut(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag"}
	_, err := a.drainSSE(context.Background(), io.NopCloser(strings.NewReader(ssePrefix)), "claude-test", noOpBus{})

	cut, ok := provider.AsStreamCut(err)
	require.True(t, ok, "got %v", err)
	assert.NoError(t, cut.Err)
	assert.Contains(t, err.Error(), "closed the stream after 6 events")
}

func TestACancelledTurnIsNotAStreamCut(t *testing.T) {
	a := &Anthropic{ReminderRenderer: "tag"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.drainSSE(ctx, &cutBody{r: strings.NewReader(ssePrefix), err: context.Canceled}, "claude-test", noOpBus{})

	_, ok := provider.AsStreamCut(err)
	assert.False(t, ok)
	assert.ErrorIs(t, err, context.Canceled)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type closedAfterStop struct {
	mu     sync.Mutex
	r      io.Reader
	closed bool
	wait   chan struct{}
	failed chan struct{}
}

func (b *closedAfterStop) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		<-b.wait
		close(b.failed)
		return 0, errors.New("http2: response body closed")
	}
	return n, err
}

func (b *closedAfterStop) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.wait)
	}
	return nil
}

// 184 of these warnings in two days buried the one real reset.
func TestClosingTheBodyAfterMessageStopLogsNothing(t *testing.T) {
	var logs lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	a := &Anthropic{ReminderRenderer: "tag"}
	body := &closedAfterStop{r: strings.NewReader(ssePrefix + sseStop), wait: make(chan struct{}), failed: make(chan struct{})}
	_, err := a.drainSSE(context.Background(), body, "claude-test", noOpBus{})
	require.NoError(t, err)
	body.Close()
	<-body.failed
	time.Sleep(50 * time.Millisecond)

	assert.NotContains(t, logs.String(), "sse body read")
}
