package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

type usageProvider struct {
	mu    sync.Mutex
	calls int
	hold  chan struct{}
	held  chan struct{}
}

func (p *usageProvider) Name() string        { return "mock" }
func (p *usageProvider) Fingerprint() string { return "mock/usage" }
func (p *usageProvider) SetModel(string)     {}
func (p *usageProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *usageProvider) Send(ctx context.Context, _ provider.SendInput, bus provider.Bus) error {
	p.mu.Lock()
	p.calls++
	n, hold, held := p.calls, p.hold, p.held
	p.mu.Unlock()
	if hold != nil {
		select {
		case held <- struct{}{}:
		default:
		}
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	msg := message.Message{
		Role:       message.RoleOutput,
		Content:    []message.Content{message.TextContent("ok")},
		StopReason: message.StopEnd,
		Usage:      &message.Usage{InputTokens: 10, CacheReadTokens: 50_000 * n, OutputTokens: 5},
	}
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func (p *usageProvider) holdNext() (held <-chan struct{}, release func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hold, p.held = make(chan struct{}), make(chan struct{}, 1)
	hold := p.hold
	return p.held, func() { close(hold) }
}

func usageDaemon(t *testing.T) (*angelus.Angelus, *sdk.Angelus, context.Context, *usageProvider) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	p := &usageProvider{}
	a, acli, ctx, _ := daemonWithProvider(t, dir, nil, func() provider.Provider { return p })
	return a, acli, ctx, p
}

func turnOn(t *testing.T, a *angelus.Angelus, id, prompt string) {
	t.Helper()
	done := make(chan struct{}, 1)
	c, err := sdk.DialAria(ariaAddr(a, id), func(method string, _ json.RawMessage) {
		if method == "turn.done" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	require.NoError(t, err)
	defer c.Close()
	_, _, err = c.Qua(context.Background(), prompt, nil)
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: the turn never finished", prompt)
	}
	waitIdle(t, a, id)
}

func listed(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) rpc.FigaroInfoResponse {
	t.Helper()
	resp, err := acli.List(ctx)
	require.NoError(t, err)
	for _, f := range resp.Figaros {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("%s is not listed", id)
	return rpc.FigaroInfoResponse{}
}

// gen4 take 35: every fork reported a context of 11 tokens, its new prompt
// alone, until its first usage landed, so `figaro ls` CTX and the agent's
// live metrics lied through the fork's first turn. An interior fork's sidecar
// was seeded with a context of zero, and the agent trusts the sidecar. Every
// fork starts from its parent's context at the fork point.
func TestAForkStartsAtItsParentsContext(t *testing.T) {
	a, acli, ctx, p := usageDaemon(t)
	created, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	parent := created.FigaroID
	for i := 1; i <= 3; i++ {
		turnOn(t, a, parent, fmt.Sprintf("prompt %d", i))
	}
	const afterTurn2, afterTurn3 = 10 + 100_000 + 5, 10 + 150_000 + 5
	require.Equal(t, afterTurn3, listed(t, acli, ctx, parent).ContextTokens)

	forkMidTurn := func() string {
		held, release := p.holdNext()
		c, err := sdk.DialAria(ariaAddr(a, parent), nil)
		require.NoError(t, err)
		defer c.Close()
		_, _, err = c.Qua(ctx, "the parent's open turn", nil)
		require.NoError(t, err)
		select {
		case <-held:
		case <-time.After(10 * time.Second):
			t.Fatal("the parent's turn never reached the provider")
		}
		fr, err := acli.Fork(ctx, parent, 0, 0, nil, nil)
		require.NoError(t, err)
		release()
		waitIdle(t, a, parent)
		return fr.Alternative
	}
	forkAt := func(turn uint64) func() string {
		return func() string {
			fr, err := acli.Fork(ctx, parent, turn, 0, nil, nil)
			require.NoError(t, err)
			return fr.Alternative
		}
	}

	for _, tc := range []struct {
		name string
		fork func() string
		want int
	}{
		{"head", forkAt(0), afterTurn3},
		{"interior at turn 3", forkAt(3), afterTurn2},
		{"head, taken mid-turn", forkMidTurn, afterTurn3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := tc.fork()

			got := listed(t, acli, ctx, child).ContextTokens
			require.GreaterOrEqual(t, got, tc.want, "ls CTX of the dormant fork")
			require.Less(t, got, tc.want+1_000, "ls CTX of the dormant fork")

			held, release := p.holdNext()
			c, err := sdk.DialAria(ariaAddr(a, child), nil)
			require.NoError(t, err)
			defer c.Close()
			_, _, err = c.Qua(ctx, "the fork's first prompt", nil)
			require.NoError(t, err)
			select {
			case <-held:
			case <-time.After(10 * time.Second):
				t.Fatal("the fork's first turn never reached the provider")
			}
			mid := listed(t, acli, ctx, child)
			m, err := c.Metrics(ctx)
			require.NoError(t, err)
			release()
			require.GreaterOrEqual(t, mid.ContextTokens, tc.want, "ls CTX mid-way through the fork's first turn")
			require.False(t, mid.ContextExact, "no usage has landed: the figure is an estimate")
			require.GreaterOrEqual(t, m.ContextTokens, tc.want, "the live metrics mid-way through the fork's first turn")
			waitIdle(t, a, child)
		})
	}
}
