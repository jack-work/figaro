package anthropic

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/forgerytest"
)

// A history translated before reminders were coalesced keeps its bytes: the
// fingerprint did not move, so the stale check spares the rows and catch-up
// renders only what is new. A figaro pays no cold write for the change.
func TestHistoryTranslatedBeforeCoalescingKeepsItsBytes(t *testing.T) {
	p := &Anthropic{Templates: mustTemplates(t)}
	require.Equal(t, "anthropic/tag/v10", p.Fingerprint(), "the v0.74.2 fingerprint")

	figLog := arialog.NewMemLog[message.Message]()
	rows := arialog.NewMemLog[[]json.RawMessage]()
	old := forgerytest.Prompted()
	lt, err := figLog.Append(arialog.Entry[message.Message]{Payload: old})
	require.NoError(t, err)
	hash, err := arialog.FigaroHash(old)
	require.NoError(t, err)
	perPart := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"\u003csystem-reminder name=\"sender\"\u003e\nfigaro\n\u003c/system-reminder\u003e"},{"type":"text","text":"go"},{"type":"text","text":"\u003csystem-reminder name=\"mantra\"\u003e\ncoalesce\n\u003c/system-reminder\u003e"},{"type":"text","text":"\u003csystem-reminder name=\"study:@r\"\u003e\n{}\n\u003c/system-reminder\u003e"}]}`)
	_, err = rows.Append(arialog.Entry[[]json.RawMessage]{
		FigaroLT: lt.LT, FigaroHash: hash, Fingerprint: "anthropic/tag/v10",
		Payload: []json.RawMessage{perPart},
	})
	require.NoError(t, err)

	require.True(t, p.invalidateIfStale(rows))
	next := forgerytest.Prompted()
	next.Patches = []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"datetime": json.RawMessage(`"later"`), "mantra": json.RawMessage(`"onward"`),
	}, nil)}
	_, err = figLog.Append(arialog.Entry[message.Message]{Payload: next})
	require.NoError(t, err)
	seq, err := p.catchUp(context.Background(), figLog, rows, nil, nil)
	require.NoError(t, err)
	got, _ := provider.CollectRows(seq)
	require.Len(t, got, 2)
	require.Equal(t, string(perPart), string(got[0]))
	forgerytest.AssertOneBlock(t, got[1:], []string{"datetime", "mantra", "study:@r"})
}
