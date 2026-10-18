package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/turns"
	"github.com/jack-work/figaro/sdk"
)

// longAria is a real angelus over a real store: aria.read caps one page at
// ariaReadHardCap, and the cap is what a whole-history reader has to page past.
func longAria(t *testing.T, n int) (*sdk.Angelus, string) {
	t.Helper()
	backend, id := store.NewTestAria(t, "long", message.Patch{})
	ir, err := backend.OpenFigIR(id)
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		_, err := ir.Append(store.Entry[message.Message]{Payload: message.Message{
			Role:    role,
			Content: []message.Content{message.TextContent(fmt.Sprintf("m%d", i))},
		}})
		require.NoError(t, err)
	}

	acli, _ := serveBackend(t, backend)
	return acli, id
}

// serveBackend runs a real angelus over backend and returns a client and the
// runtime dir its door listens in.
func serveBackend(t *testing.T, backend store.Backend) (*sdk.Angelus, string) {
	t.Helper()
	// A unix socket path is length-capped, so the runtime dir is short.
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "f")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	a := angelus.New(angelus.Config{Backend: backend, RuntimeDir: dir})
	ctx, cancel := context.WithCancel(context.Background())
	loaded, err := config.Load(t.TempDir())
	require.NoError(t, err)
	a.Handlers = angelus.NewHandlers(angelus.ServerConfig{
		Angelus: a, Config: loaded, Ctx: ctx,
		ProviderFactory: func(string, provider.Knobs) (provider.Provider, error) {
			return nil, errors.New("no provider: this aria is read, never run")
		},
	}).Map
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("angelus.Run did not return after cancel")
		}
	})

	var acli *sdk.Angelus
	require.Eventually(t, func() bool {
		acli, err = sdk.DialAngelus(transport.UnixEndpoint(a.SocketPath))
		return err == nil
	}, 3*time.Second, 10*time.Millisecond, "angelus did not come up")
	t.Cleanup(func() { acli.Close() })
	return acli, dir
}

func ordinals(t *testing.T, msgs []message.Message, n int) {
	t.Helper()
	require.Equal(t, n, len(msgs), "messages read, of %d in the aria", n)
	for i, m := range msgs {
		require.Len(t, m.Content, 1, "message %d", i)
		require.Equal(t, fmt.Sprintf("m%d", i), m.Content[0].Text, "message %d", i)
	}
}

// export must read past one page of aria.read (#23).
func TestExportMessagesReadsPastTheReadCap(t *testing.T) {
	const n = 2_507
	acli, id := longAria(t, n)

	page, err := acli.IR(context.Background(), id, 0, 0)
	require.NoError(t, err)
	require.Less(t, len(page.Entries), n, "aria.read no longer caps a page; this test is measuring nothing")
	require.NotZero(t, page.NextFrom, "a capped page must say where the rest starts")

	msgs, err := exportMessages(context.Background(), acli, id)
	require.NoError(t, err)
	ordinals(t, msgs, n)
}

// ariaMessages resolves turns client-side for `attend <id>:N` and `status`; a
// truncated read resolves against a prefix.
func TestAriaMessagesReadsPastTheReadCap(t *testing.T) {
	const n = 2_507
	acli, id := longAria(t, n)

	msgs, err := ariaMessages(context.Background(), acli, id)
	require.NoError(t, err)
	var conv []message.Message
	for _, m := range msgs {
		if m.Role == message.RoleGenesis || (m.Role == message.RoleInput && len(m.Content) == 0) {
			continue
		}
		conv = append(conv, m)
	}
	ordinals(t, conv, n)
	// LTs are stitched on, monotonic, and never the zero a forgotten stitch leaves.
	for i := 1; i < len(msgs); i++ {
		require.Greater(t, msgs[i].LogicalTime, msgs[i-1].LogicalTime, "message %d", i)
	}
	require.NotZero(t, msgs[0].LogicalTime)

	last := turns.StampIDs(msgs)
	require.Greater(t, last, uint64(1000))
	_, err = resolveTurn(context.Background(), acli, id, last)
	require.NoError(t, err)
}
