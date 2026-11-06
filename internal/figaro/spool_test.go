package figaro_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

// spoolProvider records the user text the model would see last on every Send.
// Turns block until their context ends when hold is set, and otherwise fail at
// once: these tests care what was asked, not what came back.
type spoolProvider struct {
	hold bool
	mu   sync.Mutex
	asks []string
	sent chan struct{}
}

func newSpoolProvider(hold bool) *spoolProvider {
	return &spoolProvider{hold: hold, sent: make(chan struct{}, 16)}
}

func (p *spoolProvider) Name() string        { return "mock" }
func (p *spoolProvider) Fingerprint() string { return "mock/v0" }
func (p *spoolProvider) SetModel(string)     {}
func (p *spoolProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *spoolProvider) Send(ctx context.Context, in provider.SendInput, _ provider.Bus) error {
	var texts []string
	entries := in.FigLog.Read()
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i].Payload
		if m.Role != message.RoleInput {
			break
		}
		for _, c := range m.Content {
			texts = append([]string{c.Text}, texts...)
		}
	}
	p.mu.Lock()
	p.asks = append(p.asks, strings.Join(texts, "\n"))
	p.mu.Unlock()
	p.sent <- struct{}{}
	if p.hold {
		<-ctx.Done()
		return ctx.Err()
	}
	return context.Canceled
}

func (p *spoolProvider) await(t *testing.T) string {
	t.Helper()
	select {
	case <-p.sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the provider was never asked")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asks[len(p.asks)-1]
}

func (p *spoolProvider) quiet(t *testing.T, d time.Duration, why string) {
	t.Helper()
	select {
	case <-p.sent:
		p.mu.Lock()
		defer p.mu.Unlock()
		t.Fatalf("%s: asked %q", why, p.asks[len(p.asks)-1])
	case <-time.After(d):
	}
}

func spoolAgent(t *testing.T, be store.Backend, id string, spool *figaro.Spool, p provider.Provider) *figaro.Agent {
	t.Helper()
	a := figaro.NewAgent(figaro.Config{
		Projector: uiir.New(nil),
		ID:        id,
		Provider:  p,
		Backend:   be,
		Spool:     spool,
	})
	a.Redeliver()
	return a
}

func awaitState(t *testing.T, a *figaro.Agent, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for a.Info().State != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %q, want %q", a.Info().State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func awaitQueued(t *testing.T, a *figaro.Agent, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, q := a.QueuedPrompts(false)
		if len(q) == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d queued, want %d", len(q), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stop suspends the turn in flight and keeps what is queued; the next agent
// on the same store resumes the turn and delivers the queue, with no prompt.
func TestASuspendedTurnAndItsQueueAreRedeliveredByTheNextAgent(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	spool := figaro.NewSpool(filepath.Join(t.TempDir(), "spool"))

	first := newSpoolProvider(true)
	a1 := spoolAgent(t, be, id, spool, first)
	a1.SubmitPrompt(rpc.QuaRequest{Text: "first"})
	require.Contains(t, first.await(t), "first")
	a1.SubmitPrompt(rpc.QuaRequest{Text: "second"})
	awaitQueued(t, a1, 1)

	a1.Suspend()
	awaitState(t, a1, figaro.StateSuspended)
	a1.Kill()
	first.quiet(t, 200*time.Millisecond, "a suspended agent started the queued turn")

	second := newSpoolProvider(false)
	a2 := spoolAgent(t, be, id, spool, second)
	defer a2.Kill()
	ask := second.await(t)
	require.Contains(t, ask, figaro.ResumeNotice)
	require.Contains(t, ask, "second")
}

// A prompt accepted while the agent is suspending is spooled, not run and not lost.
func TestAPromptSentWhileSuspendedWaitsForTheNextAgent(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	spool := figaro.NewSpool(filepath.Join(t.TempDir(), "spool"))

	idle := newSpoolProvider(false)
	a1 := spoolAgent(t, be, id, spool, idle)
	a1.Suspend()
	a1.SubmitPrompt(rpc.QuaRequest{Text: "late"})
	idle.quiet(t, 200*time.Millisecond, "a suspended agent ran a prompt")
	a1.Kill()

	next := newSpoolProvider(false)
	a2 := spoolAgent(t, be, id, spool, next)
	defer a2.Kill()
	ask := next.await(t)
	require.Contains(t, ask, "late")
	require.NotContains(t, ask, figaro.ResumeNotice, "nothing was cut, so nothing is resumed")
}

// A turn the user hung up is finished business: a restart must not revive it.
func TestAHungUpTurnLeavesNothingToRedeliver(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)

	first := newSpoolProvider(true)
	a1 := spoolAgent(t, be, id, spool, first)
	a1.SubmitPrompt(rpc.QuaRequest{Text: "first"})
	first.await(t)
	a1.Hangup(rpc.QueueKeep)
	awaitState(t, a1, "idle")
	a1.Suspend()
	a1.Kill()

	ids, err := spool.Arias()
	require.NoError(t, err)
	require.Empty(t, ids)

	next := newSpoolProvider(false)
	a2 := spoolAgent(t, be, id, spool, next)
	defer a2.Kill()
	next.quiet(t, 300*time.Millisecond, "a hung-up turn was resumed")
}

// The crash case in miniature: a cut left on disk is resumed once. The resume
// itself, cut by another crash, is not resumed a second time.
func TestACutIsResumedOnceAndAResumeCutByACrashIsNot(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"cut":{"turn":1}}`), 0o600))

	resumed := newSpoolProvider(true)
	a1 := spoolAgent(t, be, id, spool, resumed)
	require.Contains(t, resumed.await(t), figaro.ResumeNotice)

	path := filepath.Join(dir, id+".json")
	crashed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(crashed), `"resumed":true`, "the resume turn must be marked on disk while it runs")
	a1.Kill()
	require.NoError(t, os.WriteFile(path, crashed, 0o600))

	again := newSpoolProvider(false)
	a2 := spoolAgent(t, be, id, spool, again)
	defer a2.Kill()
	again.quiet(t, 300*time.Millisecond, "a resume cut by a crash was resumed again")
	ids, err := spool.Arias()
	require.NoError(t, err)
	require.Empty(t, ids)
}

// BenchmarkPromptToProvider is the spool's price on the qua path: a prompt
// submitted, carried to the provider, and the turn settled, with and without
// a spool under it.
func BenchmarkPromptToProvider(b *testing.B) {
	for _, spooled := range []bool{false, true} {
		name := "spool=off"
		if spooled {
			name = "spool=on"
		}
		b.Run(name, func(b *testing.B) {
			be, id := store.NewTestAria(b, "d", message.Patch{})
			var spool *figaro.Spool
			if spooled {
				spool = figaro.NewSpool(filepath.Join(b.TempDir(), "spool"))
			}
			p := newSpoolProvider(false)
			p.sent = make(chan struct{}, 1)
			a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Provider: p, Backend: be, Spool: spool})
			a.Redeliver()
			defer a.Kill()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.SubmitPrompt(rpc.QuaRequest{Text: "x"})
				<-p.sent
				for a.Info().State != "idle" {
					time.Sleep(20 * time.Microsecond)
				}
			}
		})
	}
}

// A crash between writing a spool temp file and renaming it leaves prompt text
// on disk under a dot name nothing reads; the boot sweep removes it, and a
// spool dir left with a loose mode is tightened.
func TestPrepareSweepsTempFilesAndTightensTheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".abc.123"), []byte(`{"queued":[{"text":"secret"}]}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "abc.json"), []byte(`{"queued":[{"text":"kept"}]}`), 0o600))

	spool := figaro.NewSpool(dir)
	require.NoError(t, spool.Prepare())

	_, err := os.Stat(filepath.Join(dir, ".abc.123"))
	require.True(t, os.IsNotExist(err), "the orphaned temp file survived")
	ids, err := spool.Arias()
	require.NoError(t, err)
	require.Equal(t, []string{"abc"}, ids)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// stubbornProvider never returns, cancelled or not: a turn that will not die.
type stubbornProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *stubbornProvider) Name() string        { return "mock" }
func (p *stubbornProvider) Fingerprint() string { return "mock/v0" }
func (p *stubbornProvider) SetModel(string)     {}
func (p *stubbornProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *stubbornProvider) Send(context.Context, provider.SendInput, provider.Bus) error {
	close(p.started)
	<-p.release
	return context.Canceled
}

// The brake must be on disk when Halt returns: a turn that ignores its
// cancellation can outlive every grace and die with the process, and the next
// start must not resume what the user stopped on purpose.
func TestAHaltIsDurableBeforeTheTurnItHaltsHasEnded(t *testing.T) {
	be, id := backedConv(t, t.TempDir())
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)

	stubborn := &stubbornProvider{started: make(chan struct{}), release: make(chan struct{})}
	a1 := spoolAgent(t, be, id, spool, stubborn)
	t.Cleanup(func() {
		close(stubborn.release)
		a1.Kill()
	})
	a1.SubmitPrompt(rpc.QuaRequest{Text: "runaway"})
	<-stubborn.started
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"cut"`, "a running turn is marked on disk")

	a1.Halt()
	require.Equal(t, "active", a1.Info().State, "the turn is still running: the provider ignores the cancel")
	_, err = os.Stat(filepath.Join(dir, id+".json"))
	require.True(t, os.IsNotExist(err), "the cut mark outlived the halt")

	next := newSpoolProvider(false)
	a2 := spoolAgent(t, be, id, spool, next)
	defer a2.Kill()
	next.quiet(t, 300*time.Millisecond, "a halted turn was resumed")
}
