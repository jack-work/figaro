package figaro

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

func TestRefreshMetricsIncrementalMatchesFullFold(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	cb, _ := chalkboard.Open("")
	a := &Agent{
		figLog: log, prov: perfProvider{}, chalkboard: cb,
		inbox: NewInbox(context.Background()),
	}
	sequence := []message.Message{
		{Role: message.RoleGenesis},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("first prompt")}},
		{
			Role:    message.RoleAssistant,
			Content: []message.Content{message.TextContent("reply")},
			Usage:   &message.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 40},
		},
		{Role: message.RoleUser, Content: []message.Content{message.TextContent("follow up")}},
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
