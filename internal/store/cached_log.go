package store

import (
	"sort"
	"sync"
)

// cachedLog is a memoized read-through view over a Log[T]. It materializes a
// WINDOW of the channel's entries in memory and serves reads from there,
// appending incrementally and falling through to the inner log for anything
// below the window. One cachedLog is shared per (aria, channel) by the
// backend, so a reader (aria.page) sees the writer's (agent's) appends.
//
// It used to hold every entry for the aria's whole life. That is the largest
// thing a live aria pins: the decoded fig IR runs 4-5x its encoded bytes, and
// a real 2500-message aria measured 12.5 MiB of it against 2.0 MiB of composed
// UI and 48 KiB of form.
//
// The window is a window and not an LRU on purpose. Figaro's access pattern on
// the IR is append at the tail, translate the tail (provider.TailAfter),
// render recent turns, and occasionally page backward on a user scroll. A
// window over that is a slice header and one counter; an LRU would be a map, a
// list, per-entry bookkeeping and a second lock for the same reclamation. The
// backward-paging case is served from the store by the angelus reader without
// touching this window at all.
//
// Caching stays invisible to consumers: every method below satisfies Log[T]
// with identical semantics whether or not a row is resident. What changed is
// that "all of it is in RAM" is no longer free — see the note on store.Snapshot
// in log.go.
type cachedLog[T any] struct {
	inner Log[T]
	mu    sync.RWMutex

	// rows is the resident window: the last len(rows) entries of the log.
	rows []Entry[T]
	// trimmed is how many entries were dropped off the front. Absolute index
	// of rows[i] is trimmed+i, which is what byFK stores.
	trimmed int
	// byFK maps FigaroLT to ABSOLUTE index, so trimming does not invalidate it
	// (an entry below the window is simply not resident, and its index is
	// still the truth if it comes back).
	byFK map[uint64]int

	// window is the maximum number of resident entries, enforced on append.
	// 0 disables trimming, which is the default: an unconfigured store behaves
	// exactly as it did before.
	window int

	// budget bounds resident entries in BYTES, and is the knob that actually
	// controls memory. Row count does not: measured on a real 2556-message
	// aria, dropping 80% of ROWS released only 26% of BYTES, because a long
	// agentic conversation puts its large tool results at the tail and its
	// short prose at the head. A row budget bounds the wrong axis of a skewed
	// distribution. 0 disables.
	budget int
	// sizeOf estimates one entry's retained bytes. nil means unmeasurable, in
	// which case budget is ignored and only window applies.
	sizeOf func(Entry[T]) int
	// bytes is the running size of rows, maintained incrementally so trimming
	// never has to re-measure the window.
	bytes int
}

var _ Log[any] = (*cachedLog[any])(nil)

func newCachedLog[T any](inner Log[T]) *cachedLog[T] {
	return newWindowedLog[T](inner, 0, 0, 1, nil)
}

// newWindowedLog builds a cache bounded by row count, byte budget, or both.
// Zero for either disables it; both zero retains everything.
// inflation is how much larger a decoded entry is than its encoded record, so
// the tail read's byte gate is denominated in the same units as sizeOf. Passing
// them separately is the price of gating BEFORE decode: the gate sees encoded
// bytes, the accounting sees decoded estimates, and the two must agree or the
// window holds the wrong amount.
func newWindowedLog[T any](inner Log[T], window, budget, inflation int, sizeOf func(Entry[T]) int) *cachedLog[T] {
	if inflation < 1 {
		inflation = 1
	}
	c := &cachedLog[T]{
		inner: inner, byFK: map[uint64]int{},
		window: window, budget: budget, sizeOf: sizeOf,
	}

	// A bounded cache reads only the tail it will keep, when the inner log can
	// serve one. Reading everything and compacting afterwards worked but had to
	// touch the whole channel to do it: 2556 json.Unmarshals to retain 420, and
	// a transient allocation of the full log to hold a fraction of it. Steady
	// state was bounded; the moment of opening was not, and a burst of opens
	// stacked those peaks.
	if tb, ok := inner.(tailBudgetedLog[T]); ok && (budget > 0 || window > 0) {
		rows, total := tb.TailBudgeted(budget, window, inflation)
		c.rows = rows
		c.trimmed = total - len(rows)
		for i, e := range rows {
			c.byFK[e.FigaroLT] = c.trimmed + i
			c.bytes += c.sizeOfLocked(e)
		}
		return c
	}

	for _, e := range inner.Read() {
		c.byFK[e.FigaroLT] = len(c.rows)
		c.rows = append(c.rows, e)
		c.bytes += c.sizeOfLocked(e)
	}
	// Compact EXACTLY at construction, without the append path's slack: this is
	// the moment the whole log was just materialized, so it is precisely the
	// residency the window exists to avoid.
	c.compactLocked(0)
	return c
}

func (c *cachedLog[T]) sizeOfLocked(e Entry[T]) int {
	if c.sizeOf == nil {
		return 0
	}
	return c.sizeOf(e)
}

// Read returns every entry. It falls through to the inner log when the window
// does not hold the whole channel — the honest price of a call that wants the
// prefix, and the reason nothing on the hot path calls it.
func (c *cachedLog[T]) Read() []Entry[T] {
	c.mu.RLock()
	if c.trimmed == 0 {
		out := make([]Entry[T], len(c.rows))
		copy(out, c.rows)
		c.mu.RUnlock()
		return out
	}
	c.mu.RUnlock()
	return c.inner.Read()
}

func (c *cachedLog[T]) TailSnapshot(n int) []Entry[T] {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || len(c.rows) == 0 {
		return nil
	}
	if n > len(c.rows) {
		n = len(c.rows)
	}
	return c.rows[len(c.rows)-n:]
}

func (c *cachedLog[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.trimmed + len(c.rows)
}

func (c *cachedLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	c.mu.RLock()
	if c.belowWindowLocked(figaroLT) {
		c.mu.RUnlock()
		return c.inner.ReadFrom(figaroLT, n)
	}
	defer c.mu.RUnlock()
	start := sort.Search(len(c.rows), func(i int) bool {
		return c.rows[i].FigaroLT >= figaroLT
	})
	end := len(c.rows)
	if n > 0 && start+n < end {
		end = start + n
	}
	out := make([]Entry[T], end-start)
	copy(out, c.rows[start:end])
	return out
}

// TailAfter serves the suffix without copying the prefix. rows are ascending
// by LT (channel order in, appends at the tail), so this is a binary search.
// It is the read the incremental translator uses, and the one the window is
// shaped for: a warm watermark is always inside it.
func (c *cachedLog[T]) TailAfter(lt uint64) ([]Entry[T], int) {
	c.mu.RLock()
	// A watermark below the window means a cold consumer, which needs the
	// prefix it is about to fold anyway.
	if c.trimmed > 0 && (len(c.rows) == 0 || lt < c.rows[0].LT) {
		c.mu.RUnlock()
		all := c.inner.Read()
		i := 0
		for i < len(all) && all[i].LT <= lt {
			i++
		}
		out := make([]Entry[T], len(all)-i)
		copy(out, all[i:])
		return out, len(all)
	}
	defer c.mu.RUnlock()
	start := sort.Search(len(c.rows), func(i int) bool {
		return c.rows[i].LT > lt
	})
	out := make([]Entry[T], len(c.rows)-start)
	copy(out, c.rows[start:])
	return out, c.trimmed + len(c.rows)
}

func (c *cachedLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	c.mu.RLock()
	// Paging backward past the window is the user-scroll case. Serve it from
	// disk rather than growing the window: a scroll must not permanently
	// re-resident a prefix nobody will read again.
	if c.trimmed > 0 && (before == 0 || c.belowWindowLocked(before)) {
		c.mu.RUnlock()
		return c.inner.ReadPage(from, before, n)
	}
	if c.belowWindowLocked(from) {
		c.mu.RUnlock()
		return c.inner.ReadPage(from, before, n)
	}
	defer c.mu.RUnlock()
	page, _ := readPage(c.rows, from, before, n)
	return page, c.trimmed + len(c.rows)
}

func (c *cachedLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	c.mu.RLock()
	if i, ok := c.byFK[figaroLT]; ok {
		if rel := i - c.trimmed; rel >= 0 && rel < len(c.rows) {
			e := c.rows[rel]
			c.mu.RUnlock()
			return e, true
		}
		// Known to exist, not resident.
		c.mu.RUnlock()
		return c.inner.Lookup(figaroLT)
	}
	trimmed := c.trimmed
	c.mu.RUnlock()
	if trimmed > 0 {
		return c.inner.Lookup(figaroLT)
	}
	return Entry[T]{}, false
}

// PeekTail is always resident: the window is a tail window, so the last entry
// is in it whenever the log is non-empty.
func (c *cachedLog[T]) PeekTail() (Entry[T], bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.rows) == 0 {
		return Entry[T]{}, false
	}
	return c.rows[len(c.rows)-1], true
}

func (c *cachedLog[T]) Append(e Entry[T]) (Entry[T], error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stamped, err := c.inner.Append(e)
	if err != nil {
		return Entry[T]{}, err
	}
	c.byFK[stamped.FigaroLT] = c.trimmed + len(c.rows)
	c.rows = append(c.rows, stamped)
	c.bytes += c.sizeOfLocked(stamped)
	// Trim on append rather than on a timer. A log growing through a long
	// autonomous turn has to be bounded now, not at the next sweep, and doing
	// it here costs no goroutine and no second scheduler.
	c.enforceWindowLocked()
	return stamped, nil
}

// Trim drops all but the last keep entries from the window and reports how
// many were released. keep <= 0 releases everything but the tail entry, which
// PeekTail and the append path both need.
//
// This is the reaper's control surface, reached through the optional
// windowedLog interface — the caller says when, never how.
func (c *cachedLog[T]) Trim(keep int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.trimLocked(keep)
}

// Resident reports how many entries are in the window.
func (c *cachedLog[T]) Resident() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.rows)
}

// enforceWindowLocked applies the configured cap, and does nothing when
// windowing is off. Kept distinct from trimLocked so that "no window
// configured" can never be confused with "trim to the minimum".
//
// It trims in BATCHES, letting the window overshoot by windowSlack before
// compacting back down. Trimming on every append past the cap was measured at
// 4.4 µs and 51 KB per append against 308 ns and zero allocations unwindowed —
// a 14x regression on the hottest write path in the daemon, because each
// compaction copies the whole window. Batching amortizes that copy over
// windowSlack appends, which brings it to within noise of untrimmed.
func (c *cachedLog[T]) enforceWindowLocked() int {
	return c.compactLocked(windowSlack)
}

// compactLocked drops entries off the front until both bounds are satisfied,
// allowing an overshoot of slack rows (and 2x the byte budget) first so the
// copy amortizes across appends rather than running on every one.
func (c *cachedLog[T]) compactLocked(slack int) int {
	overRows := c.window > 0 && len(c.rows) > c.window+slack
	overBytes := c.budget > 0 && c.sizeOf != nil && c.bytes > c.budget+c.budget*slackNum/slackDen
	if !overRows && !overBytes {
		return 0
	}

	keep := len(c.rows)
	if c.window > 0 && keep > c.window {
		keep = c.window
	}
	if c.budget > 0 && c.sizeOf != nil {
		// Walk back from the tail accumulating bytes; keep the newest entries
		// that fit. At least one, always: PeekTail and Append both read it.
		total, n := 0, 0
		for i := len(c.rows) - 1; i >= 0; i-- {
			sz := c.sizeOfLocked(c.rows[i])
			if n > 0 && total+sz > c.budget {
				break
			}
			total += sz
			n++
		}
		if n < keep {
			keep = n
		}
	}
	return c.trimLocked(keep)
}

// slackNum/slackDen is the byte overshoot allowed before compaction: half the
// budget. Same amortization argument as windowSlack.
const (
	slackNum = 1
	slackDen = 2
)

// windowSlack is how far the window may overshoot before it is compacted. It
// buys O(1) amortized appends at the cost of a bounded overshoot: residency
// peaks at window+slack, never above.
const windowSlack = 256

func (c *cachedLog[T]) trimLocked(keep int) int {
	if keep <= 0 {
		keep = 1 // never drop the tail: PeekTail and Append both read it
	}
	if len(c.rows) <= keep {
		return 0
	}
	drop := len(c.rows) - keep
	// Re-slice into a fresh array: keeping the old backing array alive would
	// retain exactly the entries we are trying to release.
	kept := make([]Entry[T], keep)
	copy(kept, c.rows[drop:])
	for _, e := range c.rows[:drop] {
		c.bytes -= c.sizeOfLocked(e)
	}
	c.rows = kept
	c.trimmed += drop
	return drop
}

// ResidentBytes is the estimated retained size of the window.
func (c *cachedLog[T]) ResidentBytes() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.bytes
}

// belowWindowLocked reports whether figaroLT names an entry ahead of the
// window's first resident row. Caller holds at least a read lock.
func (c *cachedLog[T]) belowWindowLocked(figaroLT uint64) bool {
	if c.trimmed == 0 || len(c.rows) == 0 {
		return false
	}
	return figaroLT < c.rows[0].FigaroLT
}

func (c *cachedLog[T]) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.inner.Clear(); err != nil {
		return err
	}
	c.rows = nil
	c.trimmed = 0
	c.bytes = 0
	c.byFK = map[uint64]int{}
	return nil
}
