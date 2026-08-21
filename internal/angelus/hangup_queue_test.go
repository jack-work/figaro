package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/transport"
)

// The hangup disposition, over a real angelus + a real aria socket: the same
// path `figaro hup` and `figaro cut` take. The unit tests prove the fold and
// the drain; this proves the wire carries the choice and brings the queue back.

// parkedProvider blocks in Send until the turn context dies, so prompts pile
// up behind a genuinely running turn.
type parkedProvider struct {
	mu      sync.Mutex
	started chan struct{}
}

func (p *parkedProvider) Name() string        { return "parked" }
func (p *parkedProvider) Fingerprint() string { return "parked/v0" }
func (p *parkedProvider) SetModel(string)     {}
func (p *parkedProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *parkedProvider) Send(ctx context.Context, _ provider.SendInput, _ provider.Bus) error {
	p.mu.Lock()
	started := p.started
	p.started = nil
	p.mu.Unlock()
	if started != nil {
		close(started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func hangupFixture(t *testing.T) (transport.Endpoint, *parkedProvider, func()) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0700))
	require.NoError(t, os.WriteFile(dir+"/outfits/parked.toml", []byte(`
[system]
provider = "parked"
model = "m"
`), 0600))

	backend, err := store.NewXwalBackend(dir+"/arias", 0)
	require.NoError(t, err)

	a := angelus.New(angelus.Config{RuntimeDir: testRuntimeDir(t, dir), Backend: backend})
	prov := &parkedProvider{started: make(chan struct{})}
	loaded, err := config.Load(dir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a, Config: loaded, Ctx: ctx,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) { return prov, nil },
	}).Map

	go a.Run(ctx)
	waitForAngelus(t, a.SocketPath)

	acli, err := angelus.DialClient(transport.UnixEndpoint(a.SocketPath))
	require.NoError(t, err)
	create, err := acli.Create(ctx, dress(t, "parked"), nil)
	require.NoError(t, err)
	acli.Close()

	ep := transport.Endpoint{Scheme: create.Endpoint.Scheme, Address: create.Endpoint.Address}
	waitForFigaro(t, ep)
	return ep, prov, func() { cancel(); a.Shutdown(0) }
}

// queueUp parks a turn and leaves `texts` waiting behind it.
func queueUp(t *testing.T, cli *figaro.Client, prov *parkedProvider, texts ...string) {
	t.Helper()
	ctx := context.Background()
	_, _, err := cli.Qua(ctx, "the turn that parks", nil)
	require.NoError(t, err)

	prov.mu.Lock()
	started := prov.started
	prov.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("provider never entered Send")
		}
	}
	for _, text := range texts {
		_, _, err := cli.Qua(ctx, text, nil)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		resp, err := cli.Queued(ctx)
		return err == nil && len(resp.Prompts) == len(texts)
	}, 3*time.Second, 20*time.Millisecond, "prompts never reached the queue")
}

func TestHangup_KeepOverTheWire(t *testing.T) {
	ep, prov, done := hangupFixture(t)
	defer done()

	cli, err := figaro.DialClient(ep, func(string, json.RawMessage) {})
	require.NoError(t, err)
	defer cli.Close()

	queueUp(t, cli, prov, "one", "two")

	resp, err := cli.Hangup(context.Background(), rpc.QueueKeep)
	require.NoError(t, err)
	require.True(t, resp.OK)
	assert.False(t, resp.Cleared)
	assert.NotEmpty(t, resp.Epoch)
	require.Len(t, resp.Queue, 1, "the waiting run comes back as the one message it became")
	assert.Equal(t, "one\n\ntwo", resp.Queue[0].Text)
}

func TestHangup_ClearOverTheWire(t *testing.T) {
	ep, prov, done := hangupFixture(t)
	defer done()

	cli, err := figaro.DialClient(ep, func(string, json.RawMessage) {})
	require.NoError(t, err)
	defer cli.Close()

	queueUp(t, cli, prov, "one", "two")

	resp, err := cli.Hangup(context.Background(), rpc.QueueClear)
	require.NoError(t, err)
	require.True(t, resp.OK)
	assert.True(t, resp.Cleared)
	require.Len(t, resp.Queue, 2, "a cleared queue comes back verbatim, one entry per message")
	assert.Equal(t, "one", resp.Queue[0].Text)
	assert.Equal(t, "two", resp.Queue[1].Text)

	// And it really is gone.
	queued, err := cli.Queued(context.Background())
	require.NoError(t, err)
	assert.Empty(t, queued.Prompts)
}

// An old client sends {} and must get the old behaviour: the turn stops, the
// queue survives.
//
// Observed on the CONVERSATION, not on the queue. The queue is the wrong
// instrument here: the moment the interrupt lands, the drain loop is free to
// lift the folded message and start answering it, so a queue read is a race -
// it passed alone and failed under load, which is how the flake announced
// itself. What "kept" actually means is that the aria goes on to ask itself
// the combined question, and that is durable.
func TestHangup_BareInterruptKeepsTheQueue(t *testing.T) {
	ep, prov, done := hangupFixture(t)
	defer done()

	cli, err := figaro.DialClient(ep, func(string, json.RawMessage) {})
	require.NoError(t, err)
	defer cli.Close()
	ctx := context.Background()

	queueUp(t, cli, prov, "one", "two")

	require.NoError(t, cli.Interrupt(ctx))

	require.Eventually(t, func() bool {
		resp, err := cli.Context(ctx)
		if err != nil {
			return false
		}
		return contextCarriesText(t, resp.Messages, "one\n\ntwo")
	}, 3*time.Second, 25*time.Millisecond,
		"the kept queue must reach the conversation as ONE combined message")
}

// contextCarriesText reports whether any message in a figaro.context response
// carries exactly this text in a prose block.
func contextCarriesText(t *testing.T, messages []interface{}, want string) bool {
	t.Helper()
	for _, m := range messages {
		raw, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var msg struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		for _, c := range msg.Content {
			if c.Text == want {
				return true
			}
		}
	}
	return false
}

// The CRUD mutators over the wire, including the refusal a stale epoch earns.
func TestQueueMutators_OverTheWire(t *testing.T) {
	ep, prov, done := hangupFixture(t)
	defer done()

	cli, err := figaro.DialClient(ep, func(string, json.RawMessage) {})
	require.NoError(t, err)
	defer cli.Close()
	ctx := context.Background()

	queueUp(t, cli, prov, "keep me", "typo", "drop me")

	listed, err := cli.QueuedAll(ctx)
	require.NoError(t, err)
	require.Len(t, listed.Prompts, 3)
	epoch := listed.Epoch

	upd, err := cli.UpdateQueued(ctx, rpc.QueueUpdateRequest{
		Epoch: epoch, ID: listed.Prompts[1].ID, Text: "fixed",
	})
	require.NoError(t, err)
	assert.Equal(t, rpc.QueueUpdated, upd.Result.Outcome)

	del, err := cli.DeleteQueued(ctx, rpc.QueueDeleteRequest{
		Epoch: epoch, IDs: []uint64{listed.Prompts[2].ID},
	})
	require.NoError(t, err)
	require.Len(t, del.Results, 1)
	assert.Equal(t, rpc.QueueDeleted, del.Results[0].Outcome)

	after, err := cli.QueuedAll(ctx)
	require.NoError(t, err)
	require.Len(t, after.Prompts, 2)
	assert.Equal(t, []string{"keep me", "fixed"},
		[]string{after.Prompts[0].Text, after.Prompts[1].Text})

	// A stale epoch is refused as a RESULT: the call itself succeeds.
	stale, err := cli.DeleteQueued(ctx, rpc.QueueDeleteRequest{
		Epoch: "not-this-generation", IDs: []uint64{after.Prompts[0].ID},
	})
	require.NoError(t, err, "a refusal is data, not a transport error")
	require.Len(t, stale.Results, 1)
	assert.Equal(t, rpc.QueueRejected, stale.Results[0].Outcome)
	assert.Equal(t, rpc.RejectStale, stale.Results[0].Reason)

	still, err := cli.QueuedAll(ctx)
	require.NoError(t, err)
	assert.Len(t, still.Prompts, 2, "a stale request must mutate nothing")
}
