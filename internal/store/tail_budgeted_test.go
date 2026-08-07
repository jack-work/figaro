package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// realAria writes n messages of the given text size into a real xwal-backed
// aria and returns the backend and id.
func realAria(t testing.TB, n, textBytes int) (*XwalBackend, string) {
	t.Helper()
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("l", message.Patch{})
	require.NoError(t, err)
	id, err := be.CreateConversation(outfit)
	require.NoError(t, err)

	log, err := be.Open(id)
	require.NoError(t, err)
	body := make([]byte, textBytes)
	for i := range body {
		body[i] = 'x'
	}
	for i := 0; i < n; i++ {
		role := message.RoleInput
		if i%2 == 1 {
			role = message.RoleOutput
		}
		_, err := log.Append(Entry[message.Message]{Payload: message.Message{
			Role:    role,
			TurnID:  uint64(i/2 + 1),
			Content: []message.Content{{Type: message.ContentProse, Text: fmt.Sprintf("%d:%s", i, body)}},
		}})
		require.NoError(t, err)
	}
	return be, id
}

// The tail read must produce exactly the tail an unbudgeted read would, and
// must report the WHOLE channel's count — a windowed cache's Len depends on it.
func TestTailBudgeted_MatchesFullRead(t *testing.T) {
	be, id := realAria(t, 60, 128)
	inner := newXwalLog[message.Message](be.Store(), id, chanIR, true)

	full := inner.Read()
	require.NotEmpty(t, full)

	for _, maxRows := range []int{1, 5, 17, len(full), len(full) + 10} {
		got, total := inner.TailBudgeted(0, maxRows, 1)
		assert.Equal(t, len(full), total, "total for maxRows=%d", maxRows)

		want := full
		if maxRows < len(full) {
			want = full[len(full)-maxRows:]
		}
		require.Len(t, got, len(want), "maxRows=%d", maxRows)
		for i := range want {
			assert.Equal(t, want[i].LT, got[i].LT, "maxRows=%d row %d LT", maxRows, i)
			// The genesis row carries no content, so compare payloads only
			// where there is one to compare.
			if len(want[i].Payload.Content) > 0 {
				require.NotEmpty(t, got[i].Payload.Content, "maxRows=%d row %d lost content", maxRows, i)
				assert.Equal(t, want[i].Payload.Content[0].Text, got[i].Payload.Content[0].Text,
					"maxRows=%d row %d payload", maxRows, i)
			}
		}
	}
}

// Ascending order, and EncodedBytes populated — the cache sizes itself with it.
func TestTailBudgeted_AscendingWithSizes(t *testing.T) {
	be, id := realAria(t, 20, 64)
	inner := newXwalLog[message.Message](be.Store(), id, chanIR, true)

	got, _ := inner.TailBudgeted(0, 8, 1)
	require.Len(t, got, 8)
	for i := 1; i < len(got); i++ {
		assert.Greater(t, got[i].LT, got[i-1].LT, "not ascending at %d", i)
	}
	for i, e := range got {
		assert.Positive(t, e.EncodedBytes, "row %d has no encoded size", i)
	}
}

// A byte budget must bind on encoded bytes times the inflation factor, and must
// always keep at least the tail entry however small the budget.
func TestTailBudgeted_ByteBudget(t *testing.T) {
	be, id := realAria(t, 40, 512)
	inner := newXwalLog[message.Message](be.Store(), id, chanIR, true)
	full := inner.Read()

	perEntry := full[len(full)-1].EncodedBytes
	require.Positive(t, perEntry)

	// Room for about three entries at inflation 1.
	got, total := inner.TailBudgeted(perEntry*3+perEntry/2, 0, 1)
	assert.Equal(t, len(full), total)
	assert.GreaterOrEqual(t, len(got), 3)
	assert.LessOrEqual(t, len(got), 5, "budget kept more than it could afford")
	assert.Equal(t, full[len(full)-1].LT, got[len(got)-1].LT, "did not keep the tail")

	// A budget smaller than one entry still yields one.
	one, _ := inner.TailBudgeted(1, 0, 1)
	assert.Len(t, one, 1)
	assert.Equal(t, full[len(full)-1].LT, one[0].LT)

	// Inflation scales the gate: 5x the factor buys ~1/5 the entries.
	inflated, _ := inner.TailBudgeted(perEntry*5, 0, 5)
	assert.LessOrEqual(t, len(inflated), 2, "inflation did not tighten the gate")
}

// The whole point: a windowed cache built over a real store must be equivalent
// to an unwindowed one on every read, while holding only the tail.
func TestWindowedOverRealStore_Equivalent(t *testing.T) {
	be, id := realAria(t, 80, 256)
	mk := func(window, budget int) *cachedLog[message.Message] {
		return newWindowedLog[message.Message](
			newXwalLog[message.Message](be.Store(), id, chanIR, true),
			window, budget, 1, irEntrySize)
	}
	full := mk(0, 0)
	win := mk(10, 0)

	require.Equal(t, 10, win.Resident(), "window not applied")
	require.Less(t, win.Resident(), full.Resident(), "window held everything")
	assert.Equal(t, full.Len(), win.Len(), "Len must report the channel")

	fullRows := full.Read()
	assert.Equal(t, len(fullRows), len(win.Read()), "Read must fall through")

	for _, i := range []int{0, 1, len(fullRows) / 2, len(fullRows) - 1} {
		probe := fullRows[i].FigaroLT
		fe, fok := full.Lookup(probe)
		we, wok := win.Lookup(probe)
		assert.Equal(t, fok, wok, "Lookup(%d) found", probe)
		assert.Equal(t, fe.LT, we.LT, "Lookup(%d)", probe)

		fa, ftot := full.TailAfter(fullRows[i].LT)
		wa, wtot := win.TailAfter(fullRows[i].LT)
		assert.Equal(t, ftot, wtot, "TailAfter(%d) total", probe)
		require.Len(t, wa, len(fa), "TailAfter(%d) len", probe)
		for j := range fa {
			assert.Equal(t, fa[j].LT, wa[j].LT, "TailAfter(%d) row %d", probe, j)
		}

		assert.Equal(t, len(full.ReadFrom(probe, 0)), len(win.ReadFrom(probe, 0)),
			"ReadFrom(%d)", probe)
	}
}

// BenchmarkOpenWindowed charges building a cache over a real store, budgeted
// and not. The budgeted form must not decode what it will not keep.
func BenchmarkOpenWindowed(b *testing.B) {
	be, id := realAria(b, 2000, 512)
	for _, budget := range []int{0, 256 << 10} {
		name := "unbounded"
		if budget > 0 {
			name = "budget=256KiB"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				c := newWindowedLog[message.Message](
					newXwalLog[message.Message](be.Store(), id, chanIR, true),
					0, budget, 1, irEntrySize)
				if c.Len() != 2002 {
					b.Fatalf("Len = %d", c.Len())
				}
			}
		})
	}
}
