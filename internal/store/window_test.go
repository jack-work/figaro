package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowFixture builds a cachedLog over n entries with the given window.
func windowFixture(t testing.TB, n, window int) (*cachedLog[uint64], []uint64) {
	t.Helper()
	fks := make([]uint64, n)
	for i := range fks {
		fks[i] = uint64((i + 1) * 10)
	}
	return newWindowedLog[uint64](buildLog(t, fks), window, 0, 1, nil), fks
}

// A window keeps only the tail resident, and Len still reports the whole log:
// residency is invisible to a consumer.
func TestWindow_BoundsResidency(t *testing.T) {
	c, all := windowFixture(t, 100, 10)

	// Construction compacts exactly; only the append path carries slack.
	assert.Equal(t, 10, c.Resident(), "window not enforced at construction")
	assert.Equal(t, 100, c.Len(), "Len must report the log, not the window")

	tail, ok := c.PeekTail()
	require.True(t, ok)
	assert.Equal(t, all[len(all)-1], tail.FigaroLT, "tail must always be resident")
}

// Unwindowed is the default and must behave exactly as before.
func TestWindow_ZeroRetainsEverything(t *testing.T) {
	c, _ := windowFixture(t, 50, 0)
	assert.Equal(t, 50, c.Resident())
	assert.Equal(t, 50, c.Len())
	assert.Len(t, c.Read(), 50)
}

// Every read must return the same answer whether or not the row is resident.
// This is the whole claim of the design: caching is an implementation detail.
func TestWindow_ReadsAgreeWithUnwindowed(t *testing.T) {
	const n = 80
	full, all := windowFixture(t, n, 0)
	win, _ := windowFixture(t, n, 8)
	require.Equal(t, 8, win.Resident())

	assert.Equal(t, fks(full.Read()), fks(win.Read()), "Read")
	assert.Equal(t, full.Len(), win.Len(), "Len")

	for _, probe := range []uint64{all[0], all[1], all[n/2], all[n-1]} {
		fe, fok := full.Lookup(probe)
		we, wok := win.Lookup(probe)
		assert.Equal(t, fok, wok, "Lookup(%d) found", probe)
		assert.Equal(t, fe.FigaroLT, we.FigaroLT, "Lookup(%d)", probe)

		assert.Equal(t, fks(full.ReadFrom(probe, 0)), fks(win.ReadFrom(probe, 0)),
			"ReadFrom(%d)", probe)

		ft, ftot := TailAfter[uint64](full, probe)
		wt, wtot := TailAfter[uint64](win, probe)
		assert.Equal(t, ftot, wtot, "TailAfter(%d) total", probe)
		assert.Equal(t, fks(ft), fks(wt), "TailAfter(%d)", probe)
	}

	// A miss must stay a miss even when the prefix is cold.
	_, ok := win.Lookup(999999)
	assert.False(t, ok, "unwindowed miss became a hit")
}

// Backward paging past the window is the user-scroll case: correct, and it
// must not permanently re-resident the prefix.
func TestWindow_PageBackwardStaysCold(t *testing.T) {
	c, all := windowFixture(t, 60, 6)
	require.Equal(t, 6, c.Resident())

	page, total := c.ReadPage(0, all[10], 5)
	assert.Equal(t, 60, total)
	assert.NotEmpty(t, page, "cold page came back empty")
	assert.Equal(t, 6, c.Resident(), "a backward page re-residented the prefix")
}

// Appends stay bounded without any sweep: a long autonomous turn cannot grow
// the window past its cap.
func TestWindow_SelfTrimsOnAppend(t *testing.T) {
	c, _ := windowFixture(t, 5, 10)
	for i := 0; i < 200; i++ {
		_, err := c.Append(Entry[uint64]{FigaroLT: uint64(10000 + i), Payload: uint64(i)})
		require.NoError(t, err)
	}
	// Appends batch their compaction, so residency sits between the cap and
	// cap+slack — bounded, which is the guarantee that matters.
	assert.LessOrEqual(t, c.Resident(), 10+windowSlack, "window grew unbounded")
	assert.Equal(t, 205, c.Len())

	// And the newest entries are the resident ones.
	tail, ok := c.PeekTail()
	require.True(t, ok)
	assert.Equal(t, uint64(10199), tail.FigaroLT)
}

// Trim is the reaper's control surface. keep<=0 must still leave the tail,
// which PeekTail and Append both read.
func TestWindow_TrimNeverDropsTheTail(t *testing.T) {
	c, _ := windowFixture(t, 40, 0)
	released := c.Trim(0)
	assert.Equal(t, 39, released)
	assert.Equal(t, 1, c.Resident())

	_, ok := c.PeekTail()
	assert.True(t, ok, "Trim(0) left no tail")

	// Trimming again releases nothing rather than erroring or going negative.
	assert.Zero(t, c.Trim(0))
}

// A trim must actually release memory, not just re-slice a retained array.
func TestWindow_TrimReleasesBackingArray(t *testing.T) {
	c, _ := windowFixture(t, 1000, 0)
	before := cap(c.rows)
	c.Trim(10)
	assert.Equal(t, 10, cap(c.rows),
		"trim kept a %d-entry array alive to hold 10 rows", before)
}

// Clear resets the window and its offset, so a fingerprint invalidation does
// not leave a phantom trimmed count.
func TestWindow_ClearResetsOffset(t *testing.T) {
	c, _ := windowFixture(t, 50, 5)
	require.Positive(t, c.trimmed)
	require.NoError(t, c.Clear())
	assert.Zero(t, c.trimmed)
	assert.Zero(t, c.Len())
	assert.Empty(t, c.Read())
}

// BenchmarkWindowTailAfter is the translator's read at each window setting.
// The point is that it is O(suffix), not O(log): the numbers should barely
// move as the log grows.
func BenchmarkWindowTailAfter(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		for _, w := range []int{0, 512} {
			name := fmt.Sprintf("msgs=%d/window=%d", n, w)
			b.Run(name, func(b *testing.B) {
				fks := make([]uint64, n)
				for i := range fks {
					fks[i] = uint64((i + 1) * 10)
				}
				c := newWindowedLog[uint64](buildLog(b, fks), w, 0, 1, nil)
				// A warm watermark two entries behind the tail. It must be
				// the channel LT, which is what TailAfter keys on.
				all := c.Read()
				watermark := all[len(all)-3].LT
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if out, _ := c.TailAfter(watermark); len(out) != 2 {
						b.Fatalf("suffix = %d, want 2", len(out))
					}
				}
			})
		}
	}
}

// BenchmarkWindowAppend charges the self-trim to the append path it rides on.
func BenchmarkWindowAppend(b *testing.B) {
	for _, w := range []int{0, 512} {
		b.Run(fmt.Sprintf("window=%d", w), func(b *testing.B) {
			c := newWindowedLog[uint64](buildLog(b, []uint64{10}), w, 0, 1, nil)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := c.Append(Entry[uint64]{FigaroLT: uint64(1000 + i)}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// sizeOf for the byte-budget tests: entry i costs its payload value in bytes,
// so a skewed distribution is expressible.
func costOf(e Entry[uint64]) int { return int(e.Payload) }

// The byte budget is the bound that matters. Row windowing is a poor proxy:
// measured on a real aria, dropping 80% of rows released 26% of bytes, because
// the large tool results cluster at the tail.
func TestWindow_ByteBudgetBoundsBytes(t *testing.T) {
	// Head entries are cheap, tail entries expensive — the real shape.
	fks := make([]uint64, 100)
	for i := range fks {
		if i < 90 {
			fks[i] = 10 // cheap prose
		} else {
			fks[i] = 1000 // fat tool results
		}
	}
	inner := NewMemLog[uint64]()
	for _, v := range fks {
		_, err := inner.Append(Entry[uint64]{FigaroLT: uint64(len(inner.Read()) + 1), Payload: v})
		require.NoError(t, err)
	}

	c := newWindowedLog[uint64](inner, 0, 3000, 1, costOf)
	assert.LessOrEqual(t, c.ResidentBytes(), 3000, "budget exceeded")
	assert.Positive(t, c.Resident(), "budget trimmed everything")
	// 3000 bytes of a 1000-byte tail buys ~3 entries, not 3000/10=300.
	assert.LessOrEqual(t, c.Resident(), 5, "budget kept entries it could not afford")

	tail, ok := c.PeekTail()
	require.True(t, ok, "budget dropped the tail")
	assert.Equal(t, uint64(1000), tail.Payload, "kept the head instead of the tail")
}

// A single entry larger than the whole budget must still be resident: the tail
// is load-bearing for PeekTail and Append.
func TestWindow_ByteBudgetKeepsOneOversizeEntry(t *testing.T) {
	inner := NewMemLog[uint64]()
	for _, v := range []uint64{10, 10, 999999} {
		_, err := inner.Append(Entry[uint64]{FigaroLT: uint64(len(inner.Read()) + 1), Payload: v})
		require.NoError(t, err)
	}
	c := newWindowedLog[uint64](inner, 0, 100, 1, costOf)
	assert.Equal(t, 1, c.Resident())
	tail, ok := c.PeekTail()
	require.True(t, ok)
	assert.Equal(t, uint64(999999), tail.Payload)
}

// Both bounds compose: whichever binds first wins.
func TestWindow_RowAndByteBoundsCompose(t *testing.T) {
	fks := make([]uint64, 200)
	for i := range fks {
		fks[i] = 10
	}
	inner := NewMemLog[uint64]()
	for _, v := range fks {
		_, err := inner.Append(Entry[uint64]{FigaroLT: uint64(len(inner.Read()) + 1), Payload: v})
		require.NoError(t, err)
	}
	// Rows allow 50, bytes allow 10 entries. Bytes bind.
	c := newWindowedLog[uint64](inner, 50, 100, 1, costOf)
	assert.LessOrEqual(t, c.Resident(), 10)
	assert.LessOrEqual(t, c.ResidentBytes(), 100)
}
