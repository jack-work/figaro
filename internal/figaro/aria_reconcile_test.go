package figaro_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/figaro"
	ariaLog "github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
)

type canonicalThenFrameProvider struct{}

func (canonicalThenFrameProvider) Name() string        { return "canonical-frame" }
func (canonicalThenFrameProvider) Fingerprint() string { return "canonical-frame/v1" }
func (canonicalThenFrameProvider) SetModel(string)     {}
func (canonicalThenFrameProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (canonicalThenFrameProvider) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	msg := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{message.TextContent("canonical assistant")},
		StopReason: message.StopEnd, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

func TestPanicAfterIRBeforeLiveCommitReconcilesCanonicalAssistant(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: canonicalThenFrameProvider{}, Backend: b, Tools: tool.NewRegistry(),
	})
	defer a.Kill()
	a.Subscribe(&panicOnceNotifier{})
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "crashed and was restarted")

	read := a.Read(0)
	require.Nil(t, read.Live)
	require.NotEmpty(t, read.Committed)
	last := read.Committed[len(read.Committed)-1]
	assert.Equal(t, "assistant", last.Role)
	require.NotEmpty(t, last.Nodes)
	assert.Contains(t, last.Nodes[0].Markdown, "canonical assistant")
}

func TestJournalSealFailureDropsNonCanonicalLiveUnit(t *testing.T) {
	real, id := newBackedConversation(t)
	defer real.Close()
	prov := &interruptProvider{mode: "prose", started: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{
		ID: id, Provider: prov,
		Backend: journalBackend{Backend: real, journal: &failingJournal{failAfter: 2}},
		Tools:   tool.NewRegistry(), Chalkboard: mustChalkboard(t),
	})
	defer a.Kill()
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "go"})
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not stream")
	}

	time.Sleep(100 * time.Millisecond)
	a.Interrupt()
	reason := waitDoneReason(t, ch)
	assert.Contains(t, reason, "checkpoint failed")

	history := a.Context()
	require.NotEmpty(t, history)
	assert.Equal(t, message.RoleUser, history[len(history)-1].Role)
	read := a.Read(0)
	assert.Nil(t, read.Live)
	require.Len(t, read.Committed, 1)
	assert.Equal(t, "user", read.Committed[0].Role)
}

type panicQueueProvider struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (*panicQueueProvider) Name() string                                         { return "panic-queue" }
func (*panicQueueProvider) Fingerprint() string                                  { return "panic-queue/v1" }
func (*panicQueueProvider) SetModel(string)                                      {}
func (*panicQueueProvider) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *panicQueueProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	if p.calls.Add(1) == 1 {
		close(p.started)
		<-p.release
		bus.PushDelta(message.TextContent("panic frame"))
		<-ctx.Done()
		return ctx.Err()
	}
	msg := message.Message{
		Role: message.RoleAssistant, StopReason: message.StopEnd,
		Content: []message.Content{message.TextContent("queued prompt completed")}, Timestamp: time.Now().UnixMilli(),
	}
	if _, err := in.FigLog.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
		return err
	}
	bus.PushFigaro(msg)
	return nil
}

type queuePanicNotifier struct {
	once     sync.Once
	panicked chan struct{}
}

func (n *queuePanicNotifier) Notify(method string, params any) error {
	if method != rpc.MethodAriaFrame {
		return nil
	}
	read, ok := params.(ariaLog.AriaRead)
	if !ok || read.Live == nil || read.Live.Role != "assistant" {
		return nil
	}
	n.once.Do(func() {
		close(n.panicked)
		panic("queued-event recovery panic")
	})
	return nil
}

func TestPanicRecoveryPreservesQueuedPromptAndFork(t *testing.T) {
	b, id := newBackedConversation(t)
	defer b.Close()
	prov := &panicQueueProvider{started: make(chan struct{}), release: make(chan struct{})}
	a := figaro.NewAgent(figaro.Config{ID: id, Provider: prov, Backend: b, Tools: tool.NewRegistry()})
	defer a.Kill()
	notifier := &queuePanicNotifier{panicked: make(chan struct{})}
	a.Subscribe(notifier)
	ch, _ := subscribeChan(a)
	a.SubmitPrompt(rpc.QuaRequest{Text: "first"})
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first provider round did not start")
	}

	a.SubmitPrompt(rpc.QuaRequest{Text: "second"})
	forkRan := make(chan struct{})
	forkDone := make(chan error, 1)
	go func() {
		forkDone <- a.CoordinateFork(func() error {
			close(forkRan)
			return nil
		})
	}()
	close(prov.release)
	select {
	case <-notifier.panicked:
	case <-time.After(5 * time.Second):
		t.Fatal("notifier did not panic")
	}
	select {
	case <-forkRan:
	case <-time.After(5 * time.Second):
		t.Fatal("queued fork was not serviced after panic")
	}
	require.NoError(t, <-forkDone)

	deadline := time.After(5 * time.Second)
	done := 0
	for done < 2 {
		select {
		case <-deadline:
			t.Fatalf("received %d turn completions, want panic and queued prompt", done)
		case notification := <-ch:
			if notification.Method == rpc.MethodTurnDone {
				done++
			}
		}
	}
	history := a.Context()
	var sawSecond, sawCompletion bool
	for _, msg := range history {
		for _, content := range msg.Content {
			sawSecond = sawSecond || content.Text == "second"
			sawCompletion = sawCompletion || content.Text == "queued prompt completed"
		}
	}
	assert.True(t, sawSecond)
	assert.True(t, sawCompletion)
}
