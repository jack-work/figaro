package angelus_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
)

func importListed(t *testing.T, acli *sdk.Angelus, ctx context.Context, id string) rpc.FigaroInfoResponse {
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

func importedTurns(n int, trailingPrompt bool) []message.Message {
	var msgs []message.Message
	for i := 1; i <= n; i++ {
		msgs = append(msgs,
			message.Message{Role: message.RoleInput, TurnID: uint64(i), Content: []message.Content{message.TextContent(fmt.Sprintf("verse %d", i))}},
			message.Message{Role: message.RoleOutput, TurnID: uint64(i), Content: []message.Content{message.TextContent("sung")}, StopReason: message.StopEnd,
				Usage: &message.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 50_000 * i}})
	}
	if trailingPrompt {
		msgs = append(msgs, message.Message{Role: message.RoleInput, TurnID: uint64(n + 1), Content: []message.Content{message.TextContent("an encore, unanswered")}})
	}
	return msgs
}

// An imported aria listed a context of 0 (ls "-") until something woke it:
// import wrote a sidecar with no context and no watermark, which the heal
// reads as a pre-watermark sidecar and never folds, and a head fork of it
// copied the 0. Import writes what its log implies.
func TestAnImportedAriaListsTheContextItsLogImplies(t *testing.T) {
	a, acli, ctx := daemonFixture(t)
	for _, tc := range []struct {
		name     string
		msgs     []message.Message
		exact    bool
		atLeast  int
		atMost   int
		outCount int
	}{
		{"closed by a usage", importedTurns(3, false), true, 150_015, 150_015, 3},
		{"a prompt past the last usage", importedTurns(3, true), false, 150_016, 150_100, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := acli.Import(ctx, rpc.ImportRequest{Outfit: "mock", Messages: tc.msgs})
			require.NoError(t, err)
			id := resp.FigaroID

			got := importListed(t, acli, ctx, id)
			require.GreaterOrEqual(t, got.ContextTokens, tc.atLeast, "ls CTX of the imported aria")
			require.LessOrEqual(t, got.ContextTokens, tc.atMost, "ls CTX of the imported aria")
			require.Equal(t, tc.exact, got.ContextExact)

			meta, err := a.Backend.Meta(id)
			require.NoError(t, err)
			log, err := a.Backend.OpenFigIR(id)
			require.NoError(t, err)
			tail, ok := log.PeekTail()
			require.True(t, ok)
			require.Equal(t, tail.LT, meta.LastFigaroLT, "the sidecar's watermark is the log's tail, so later appends heal")
			require.Equal(t, tc.outCount, meta.TurnCount)
			require.Equal(t, 300_000, meta.CacheReadTokens, "usage totals")

			fr, err := acli.Fork(ctx, id, 0, 0, nil, nil)
			require.NoError(t, err)
			require.Equal(t, got.ContextTokens, importListed(t, acli, ctx, fr.Alternative).ContextTokens, "a head fork of the import")
		})
	}
}
