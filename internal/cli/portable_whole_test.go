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
	"github.com/jack-work/figaro/sdk"
)

// longAria is a real angelus over a real store holding an aria with n
// conversation messages: input and output alternating, each stamped with
// its ordinal so a reader can prove it saw every one, in order. It returns
// the angelus client and the aria id.
//
// The angelus is the subject, not a fake of it: aria.read caps one page at
// ariaReadHardCap (1000), and the cap is what a whole-history reader has to
// page past. A fake that copied the cap would only agree with itself.
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

	// A unix socket path is length-capped, so keep the runtime dir short.
	dir, err := os.MkdirTemp("/var/tmp", "figexp")
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
	return acli, id
}

// ordinals asserts msgs is exactly m0..m{n-1} in order.
func ordinals(t *testing.T, msgs []message.Message, n int) {
	t.Helper()
	require.Equal(t, n, len(msgs), "messages read, of %d in the aria", n)
	for i, m := range msgs {
		require.Len(t, m.Content, 1, "message %d", i)
		require.Equal(t, fmt.Sprintf("m%d", i), m.Content[0].Text, "message %d", i)
	}
}

// Issue #23: `figaro export` stopped at one page of aria.read (998 messages
// once the two scaffolding tics were dropped), reported the truncated count
// as the whole aria, and exited 0. The whole history must come out.
func TestExportMessagesReadsPastTheReadCap(t *testing.T) {
	const n = 2_507 // two full pages and a partial third
	acli, id := longAria(t, n)

	// The fixture can fail: one page is genuinely short of the aria.
	page, err := acli.IR(context.Background(), id, 0, 0)
	require.NoError(t, err)
	require.Less(t, len(page.Entries), n, "aria.read no longer caps a page; this test is measuring nothing")
	require.NotZero(t, page.NextFrom, "a capped page must say where the rest starts")

	msgs, err := exportMessages(context.Background(), acli, id)
	require.NoError(t, err)
	ordinals(t, msgs, n)
}

// The twin: turn resolution for `figaro fork <id>:N` and `send <id>:N` reads
// the whole log through ariaMessages, and a truncated read resolves turn N
// against a prefix, or reports "no turn N" for a turn that exists.
func TestAriaMessagesReadsPastTheReadCap(t *testing.T) {
	const n = 2_507
	acli, id := longAria(t, n)

	msgs, err := ariaMessages(context.Background(), acli, id)
	require.NoError(t, err)
	ordinals(t, msgs, n)
	// LTs are stitched on, monotonic, and none is the zero a forgotten stitch
	// leaves behind.
	for i := 1; i < len(msgs); i++ {
		require.Greater(t, msgs[i].LogicalTime, msgs[i-1].LogicalTime, "message %d", i)
	}
	require.NotZero(t, msgs[0].LogicalTime)
}
