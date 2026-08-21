package figaro

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

func TestRefreshMetricsIncrementalMatchesFullFold(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	cb, _ := form.Open("")
	a := &Agent{
		figLog: log, form: cb,
		inbox: NewInbox(context.Background()),
	}
	a.bindProvider(perfProvider{})
	sequence := []message.Message{
		{Role: message.RoleGenesis},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first prompt")}},
		{
			Role:    message.RoleOutput,
			Content: []message.Content{message.TextContent("reply")},
			Usage:   &message.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40},
		},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("follow up")}},
		{
			// Cache-heavy shape: the prompt is nearly all cache read, so the
			// incremental fast path and the full fold must both count all four
			// buckets or they diverge by orders of magnitude.
			Role:    message.RoleOutput,
			Content: []message.Content{message.TextContent("second reply")},
			Usage:   &message.Usage{InputTokens: 120, OutputTokens: 25, CacheReadTokens: 130_000, CacheWriteTokens: 4_000},
		},
	}
	for _, msg := range sequence {
		if _, err := log.Append(store.Entry[message.Message]{Payload: msg}); err != nil {
			t.Fatal(err)
		}
		a.refreshMetrics()
		msgs := a.Context()
		wantContext, wantExact := tokens.ContextSize(msgs)
		wantIn, wantOut, wantRead, wantWrite := sumUsage(msgs)
		info := a.Info()
		if info.MessageCount != message.CountMessages(msgs) ||
			info.TokensIn != wantIn || info.TokensOut != wantOut ||
			info.CacheReadTokens != wantRead || info.CacheWriteTokens != wantWrite ||
			info.ContextTokens != wantContext || info.ContextExact != wantExact {
			t.Fatalf("incremental metrics = %+v, full context=(%d,%t) usage=(%d,%d,%d,%d) count=%d",
				info, wantContext, wantExact, wantIn, wantOut, wantRead, wantWrite, message.CountMessages(msgs))
		}
	}

	if err := log.Clear(); err != nil {
		t.Fatal(err)
	}
	a.refreshMetrics()
	info := a.Info()
	if info.MessageCount != 0 || info.ContextTokens != 0 || !info.ContextExact {
		t.Fatalf("metrics after reset = %+v", info)
	}
}
