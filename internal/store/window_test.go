package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// windowFixture builds a cachedLog over n entries with the given window.
func windowFixture(t testing.TB, n, window int) (*treeLog[uint64], []uint64) {
	t.Helper()
	fks := make([]uint64, n)
	for i := range fks {
		fks[i] = uint64((i + 1) * 10)
	}
	return newWindowedLog[uint64](buildLog(t, fks), window, 0, 1, 1, nil), fks
}

// Unwindowed is the default and must behave exactly as before.
func TestWindow_ZeroRetainsEverything(t *testing.T) {
	c, _ := windowFixture(t, 50, 0)
	assert.Equal(t, 50, c.Len())
	assert.Len(t, c.Read(), 50)
}

// Every read must return the same answer whether or not the row is resident.
// This is the whole claim of the design: caching is an implementation detail.
func TestWindow_ReadsAgreeWithUnwindowed(t *testing.T) {
	const n = 80
	full, all := windowFixture(t, n, 0)
	win, _ := windowFixture(t, n, 8)
	win.ReadFrom(1, 0) // residency is demand-driven: read it through the cache

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
	before := c.Resident()

	page, total := c.ReadPage(0, all[10], 5)
	assert.Equal(t, 60, total)
	assert.NotEmpty(t, page, "cold page came back empty")
	assert.Equal(t, before, c.Resident(), "a backward page re-residented the prefix")
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
				c := newWindowedLog[uint64](buildLog(b, fks), w, 0, 1, 1, nil)
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
			c := newWindowedLog[uint64](buildLog(b, []uint64{10}), w, 0, 1, 1, nil)
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
	// Head entries are cheap, tail entries expensive: the real shape.
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

	c := newWindowedLog[uint64](inner, 0, 3000, 1, 1, costOf)
	c.ReadFrom(1, 0) // residency is demand-driven
	c.cache.Budget().Settle(2 * time.Second)
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
	c := newWindowedLog[uint64](inner, 0, 100, 1, 1, costOf)
	assert.LessOrEqual(t, c.Resident(), 64)
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
	c := newWindowedLog[uint64](inner, 50, 100, 1, 1, costOf)
	assert.LessOrEqual(t, c.Resident(), 10)
	assert.LessOrEqual(t, c.ResidentBytes(), 100)
}

// The budget has to reach the CACHE, not just the config struct. A window set
// after the first handle is built would leave that aria unbounded for the
// daemon's life, which is the bug the IR window's own comment warns about.
func TestTranslationBudgetReachesTheCache(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	be.SetTranslationBudget(4 << 10)

	outfit, err := be.CreateOutfit("t", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	log, err := be.OpenTranslator(id, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	big := json.RawMessage(`"` + strings.Repeat("x", 900) + `"`)
	for i := 0; i < 40; i++ {
		// FigaroLT is the coordinate a translation is addressed by -- the IR
		// record it translates -- and production always stamps it. A row
		// without one cannot be found by ReadFrom or Lookup either.
		if _, err := log.Append(Entry[[]json.RawMessage]{
			FigaroLT: uint64(i + 1), Payload: []json.RawMessage{big},
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := log.(*treeLog[[]json.RawMessage])
	c.ReadFrom(1, 0) // residency is demand-driven
	c.cache.Budget().Settle(2 * time.Second)
	if got := c.ResidentBytes(); got > 4<<10+4<<10/2+1024 {
		t.Fatalf("resident %d bytes against a 4 KiB budget: the budget did not reach the cache", got)
	}
	if c.Resident() == 0 || c.Len() != 40 {
		t.Fatalf("bounded residency must not lose records: resident=%d len=%d", c.Resident(), c.Len())
	}
	// And the trimmed prefix is still readable, from the log.
	if all := c.Read(); len(all) != 40 {
		t.Fatalf("read below the window returned %d of 40", len(all))
	}
}

// The OUTFIT column's memo outlives the form it came from, because reading a
// label opens a form, which hydrates a whole node in figwal. It must still be
// CORRECT: a write that changes the outfit has to drop it.
func TestLabelMemoIsInvalidatedByAWrite(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("first", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(id, patchSet(map[string]string{
		"system.outfit_name": "first", "system.outfit_version": "v1",
	})); err != nil {
		t.Fatal(err)
	}
	n := NodeView{ID: id}
	be.label(&n)
	if n.Outfit != "first" {
		t.Fatalf("label = %q, want first", n.Outfit)
	}
	if _, err := be.ApplyForm(id, patchSet(map[string]string{
		"system.outfit_name": "second", "system.outfit_version": "v2",
	})); err != nil {
		t.Fatal(err)
	}
	n2 := NodeView{ID: id}
	be.label(&n2)
	if n2.Outfit != "second" || n2.Version != "v2" {
		t.Fatalf("label after a redress = %q/%q, want second/v2: the memo went stale",
			n2.Outfit, n2.Version)
	}
	// A write that names no outfit key must not cost the memo.
	if _, err := be.ApplyForm(id, patchSet(map[string]string{"brief": "x"})); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	_, cached := be.labels[id]
	be.mu.Unlock()
	if !cached {
		t.Fatal("an unrelated write dropped the memo: the listing pays for every patch")
	}
}

// Recency is memoized because answering it per row HYDRATES a cold node.
// The memo must move when the aria is written to, or `fig ls` sorts by a
// timestamp that stopped advancing.
func TestRecencyMemoAdvancesOnAWrite(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("r", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	id, err := be.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := be.ApplyForm(id, kv("brief", "one")); err != nil {
		t.Fatal(err)
	}
	first := be.LastTS(id)
	if first == 0 {
		t.Fatal("a written aria reports no recency")
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := be.ApplyForm(id, kv("brief", "two")); err != nil {
		t.Fatal(err)
	}
	second := be.LastTS(id)
	if second <= first {
		t.Fatalf("recency did not advance on a board write: %d -> %d", first, second)
	}
	// An IR append must move it too.
	log, err := be.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleOutput, Content: []message.Content{message.TextContent("hi")},
	}}); err != nil {
		t.Fatal(err)
	}
	if third := be.LastTS(id); third <= second {
		t.Fatalf("recency did not advance on an IR append: %d -> %d", second, third)
	}
}
