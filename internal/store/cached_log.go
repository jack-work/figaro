package store

import (
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
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
// that "all of it is in RAM" is no longer free: see the note on store.Snapshot
// in log.go.
// logView is the published window: immutable once stored, so a reader takes
// it with one atomic load and holds no lock at all.
type logView[T any] struct {
	// rows is the resident window: the last len(rows) entries of the log.
	rows []Entry[T]
	// trimmed is how many entries were dropped off the front. The absolute
	// index of rows[i] is trimmed+i.
	trimmed int
	// bytes is the running size of rows, carried forward so trimming never
	// has to re-measure the window.
	bytes int
}

type cachedLog[T any] struct {
	inner Log[T]
	// writeMu serializes MUTATORS so cache updates land in log order. No
	// reader ever takes it: holding a lock across inner.Append would block
	// every reader for the length of an fsync, which is milliseconds now
	// that figaro syncs before it publishes.
	writeMu sync.Mutex
	// view is the whole of the cache's state. Readers load it; mutators
	// build a successor and store it. Replaces an RWMutex over rows,
	// trimmed, bytes and a byFK index: 34 acquisitions on the hot read path,
	// every one of which waited behind an append.
	view atomic.Pointer[logView[T]]

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
}

var _ Log[any] = (*cachedLog[any])(nil)

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
	c := &cachedLog[T]{inner: inner, window: window, budget: budget, sizeOf: sizeOf}
	v := &logView[T]{}

	// A bounded cache reads only the tail it will keep, when the inner log can
	// serve one. Reading everything and evicting afterwards worked but had to
	// touch the whole channel to do it: 2556 json.Unmarshals to retain 420, and
	// a transient allocation of the full log to hold a fraction of it. Steady
	// state was bounded; the moment of opening was not, and a burst of opens
	// stacked those peaks.
	if tb, ok := inner.(tailBudgetedLog[T]); ok && (budget > 0 || window > 0) {
		rows, total := tb.TailBudgeted(budget, window, inflation)
		v.rows = rows
		v.trimmed = total - len(rows)
		for _, e := range rows {
			v.bytes += c.size(e)
		}
		c.view.Store(v)
		return c
	}

	v.rows = inner.Read()
	for _, e := range v.rows {
		v.bytes += c.size(e)
	}
	// Compact EXACTLY at construction, without the append path's slack: this is
	// the moment the whole log was just materialized, so it is precisely the
	// residency the window exists to avoid.
	c.evictWindow(v, 0)
	c.view.Store(v)
	return c
}

func (c *cachedLog[T]) size(e Entry[T]) int {
	if c.sizeOf == nil {
		return 0
	}
	return c.sizeOf(e)
}

func (c *cachedLog[T]) load() *logView[T] { return c.view.Load() }

// Read returns every entry. It falls through to the inner log when the window
// does not hold the whole channel: the honest price of a call that wants the
// prefix, and the reason nothing on the hot path calls it.
func (c *cachedLog[T]) Read() []Entry[T] {
	v := c.load()
	if v.trimmed > 0 {
		return c.inner.Read()
	}
	out := make([]Entry[T], len(v.rows))
	copy(out, v.rows)
	return out
}

func (c *cachedLog[T]) TailSnapshot(n int) []Entry[T] {
	v := c.load()
	if n <= 0 || len(v.rows) == 0 {
		return nil
	}
	if n > len(v.rows) {
		n = len(v.rows)
	}
	return v.rows[len(v.rows)-n:]
}

func (c *cachedLog[T]) Len() int {
	v := c.load()
	return v.trimmed + len(v.rows)
}

func (c *cachedLog[T]) ReadFrom(figaroLT uint64, n int) []Entry[T] {
	v := c.load()
	if v.belowWindow(figaroLT) {
		return c.inner.ReadFrom(figaroLT, n)
	}
	start := sort.Search(len(v.rows), func(i int) bool {
		return v.rows[i].FigaroLT >= figaroLT
	})
	end := len(v.rows)
	if n > 0 && start+n < end {
		end = start + n
	}
	out := make([]Entry[T], end-start)
	copy(out, v.rows[start:end])
	return out
}

// TailAfter serves the suffix without copying the prefix. rows are ascending
// by LT (channel order in, appends at the tail), so this is a binary search.
// It is the read the incremental translator uses, and the one the window is
// shaped for: a warm watermark is always inside it.
func (c *cachedLog[T]) TailAfter(lt uint64) ([]Entry[T], int) {
	v := c.load()
	// A watermark below the window means a cold consumer, which needs the
	// prefix it is about to fold anyway.
	if v.trimmed > 0 && (len(v.rows) == 0 || lt < v.rows[0].LT) {
		all := c.inner.Read()
		i := 0
		for i < len(all) && all[i].LT <= lt {
			i++
		}
		out := make([]Entry[T], len(all)-i)
		copy(out, all[i:])
		return out, len(all)
	}
	start := sort.Search(len(v.rows), func(i int) bool {
		return v.rows[i].LT > lt
	})
	out := make([]Entry[T], len(v.rows)-start)
	copy(out, v.rows[start:])
	return out, v.trimmed + len(v.rows)
}

func (c *cachedLog[T]) ReadPage(from, before uint64, n int) ([]Entry[T], int) {
	v := c.load()
	// Paging backward past the window is the user-scroll case. Serve it from
	// disk rather than growing the window: a scroll must not permanently
	// re-resident a prefix nobody will read again.
	if v.trimmed > 0 && (before == 0 || v.belowWindow(before)) {
		return c.inner.ReadPage(from, before, n)
	}
	if v.belowWindow(from) {
		return c.inner.ReadPage(from, before, n)
	}
	page, _ := readPage(v.rows, from, before, n)
	return page, v.trimmed + len(v.rows)
}

// Lookup finds an entry by FigaroLT.
//
// There used to be a byFK map from FigaroLT to absolute index beside the
// window. It answered nothing the rows cannot: entries are ascending by
// FigaroLT (ReadFrom binary searches on exactly that), so a resident hit is a
// search, and every miss goes to the inner log whenever anything was trimmed.
// What it did do was grow forever - it was never pruned on trim - so a
// bounded window carried an unbounded index of the entries it had dropped.
func (c *cachedLog[T]) Lookup(figaroLT uint64) (Entry[T], bool) {
	v := c.load()
	// The LAST match, not the first: the map it replaces kept the newest
	// index written for a FigaroLT.
	i := sort.Search(len(v.rows), func(i int) bool {
		return v.rows[i].FigaroLT > figaroLT
	}) - 1
	if i >= 0 && v.rows[i].FigaroLT == figaroLT {
		return v.rows[i], true
	}
	if v.trimmed > 0 {
		return c.inner.Lookup(figaroLT)
	}
	return Entry[T]{}, false
}

// PeekTail is always resident: the window is a tail window, so the last entry
// is in it whenever the log is non-empty.
func (c *cachedLog[T]) PeekTail() (Entry[T], bool) {
	v := c.load()
	if len(v.rows) == 0 {
		return Entry[T]{}, false
	}
	return v.rows[len(v.rows)-1], true
}

func (c *cachedLog[T]) Append(e Entry[T]) (Entry[T], error) {
	// The durable half runs with NO reader blocked: an append syncs, and a
	// sync is milliseconds. Mutators serialize on writeMu so the cache still
	// sees them in log order.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	stamped, err := c.inner.Append(e)
	if err != nil {
		return Entry[T]{}, err
	}
	old := c.load()
	// append() may write into the old array's spare capacity, at an index
	// past the published length, where no reader can see it. The successor
	// is what publishes it.
	next := &logView[T]{
		rows:    append(old.rows, stamped),
		trimmed: old.trimmed,
		bytes:   old.bytes + c.size(stamped),
	}
	// Trim on append rather than on a timer. A log growing through a long
	// autonomous turn has to be bounded now, not at the next sweep, and doing
	// it here costs no goroutine and no second scheduler.
	c.evictWindow(next, windowSlack)
	c.view.Store(next)
	return stamped, nil
}

// Trim drops all but the last keep entries from the window and reports how
// many were released. keep <= 0 releases everything but the tail entry, which
// PeekTail and the append path both need.
//
// This is the reaper's control surface, reached through the optional
// windowedLog interface: the caller says when, never how.
func (c *cachedLog[T]) Trim(keep int) int {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	next := c.load().clone()
	dropped := c.trim(next, keep)
	if dropped > 0 {
		c.view.Store(next)
	}
	return dropped
}

// Resident reports how many entries are in the window.
func (c *cachedLog[T]) Resident() int { return len(c.load().rows) }

// evictWindow drops entries off the front of v until both bounds are satisfied,
// allowing an overshoot of slack rows (and 2x the byte budget) first so the
// copy amortizes across appends rather than running on every one.
//
// It trims in BATCHES. Trimming on every append past the cap was measured at
// 4.4 µs and 51 KB per append against 308 ns and zero allocations unwindowed -
// a 14x regression on the hottest write path in the daemon, because each
// eviction copies the whole window. Batching amortizes that copy over
// windowSlack appends, which brings it to within noise of untrimmed.
func (c *cachedLog[T]) evictWindow(v *logView[T], slack int) int {
	overRows := c.window > 0 && len(v.rows) > c.window+slack
	overBytes := c.budget > 0 && c.sizeOf != nil && v.bytes > c.budget+c.budget*slackNum/slackDen
	if !overRows && !overBytes {
		return 0
	}

	keep := len(v.rows)
	if c.window > 0 && keep > c.window {
		keep = c.window
	}
	if c.budget > 0 && c.sizeOf != nil {
		// Walk back from the tail accumulating bytes; keep the newest entries
		// that fit. At least one, always: PeekTail and Append both read it.
		total, n := 0, 0
		for i := len(v.rows) - 1; i >= 0; i-- {
			sz := c.size(v.rows[i])
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
	return c.trim(v, keep)
}

// slackNum/slackDen is the byte overshoot allowed before eviction: half the
// budget. Same amortization argument as windowSlack.
const (
	slackNum = 1
	slackDen = 2
)

// windowSlack is how far the window may overshoot before it is evicted down. It
// buys O(1) amortized appends at the cost of a bounded overshoot: residency
// peaks at window+slack, never above.
const windowSlack = 256

func (c *cachedLog[T]) trim(v *logView[T], keep int) int {
	if keep <= 0 {
		keep = 1 // never drop the tail: PeekTail and Append both read it
	}
	if len(v.rows) <= keep {
		return 0
	}
	drop := len(v.rows) - keep
	// Re-slice into a fresh array: keeping the old backing array alive would
	// retain exactly the entries we are trying to release.
	kept := make([]Entry[T], keep)
	copy(kept, v.rows[drop:])
	for _, e := range v.rows[:drop] {
		v.bytes -= c.size(e)
	}
	v.rows = kept
	v.trimmed += drop
	return drop
}

func (v *logView[T]) clone() *logView[T] {
	out := *v
	return &out
}

// ResidentBytes is the estimated retained size of the window.
func (c *cachedLog[T]) ResidentBytes() int { return c.load().bytes }

// belowWindow reports whether figaroLT names an entry ahead of the window's
// first resident row.
func (v *logView[T]) belowWindow(figaroLT uint64) bool {
	if v.trimmed == 0 || len(v.rows) == 0 {
		return false
	}
	return figaroLT < v.rows[0].FigaroLT
}

func (c *cachedLog[T]) Clear() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.inner.Clear(); err != nil {
		return err
	}
	c.view.Store(&logView[T]{})
	return nil
}

// --- seeding a fork from an ancestor that already decoded the prefix ---

// residentBelow returns the resident rows strictly below figaroLT, as a slice
// of the published view. The rows are never mutated after publication (the
// held-view law), so handing them out is safe and costs a slice header.
func (c *cachedLog[T]) residentBelow(figaroLT uint64) []Entry[T] {
	v := c.load()
	k := sort.Search(len(v.rows), func(i int) bool { return v.rows[i].FigaroLT >= figaroLT })
	if k == 0 {
		return nil
	}
	return v.rows[:k]
}

// newSeededLog builds a cache whose resident prefix is DONATED by an ancestor
// instead of decoded a second time. seed must be the ancestor's resident rows
// below the child's fork base, ascending.
//
// Measured, not assumed: opening a fork re-decodes the shared prefix and mints
// every string the parent already holds (decode_duplication_test.go), and a
// shallow copy shares all of them. So the child pays slice headers where it
// used to pay a decode.
//
// IT DEGRADES TO A MISS, NEVER TO A LIE. Every doubt -- an empty seed, a
// non-ascending one, a seam that does not match the log -- falls back to the
// ordinary decoding constructor. The seam is verified by reading the last
// seeded record back out of the log and comparing it: one decode, once per
// open, against serving another aria's history from memory.
//
// trimmed is derived as Len() - len(rows). Len() counts RECORDS and rows
// counts records that decoded, so trimmed can only ever be an OVER-estimate,
// which sends a read to disk that memory could have served. The opposite error
// would report a truncated window as complete.
func newSeededLog[T any](inner Log[T], window, budget, inflation int, sizeOf func(Entry[T]) int, seed []Entry[T]) *cachedLog[T] {
	if len(seed) == 0 || !ascending(seed) {
		return newWindowedLog(inner, window, budget, inflation, sizeOf)
	}
	last := seed[len(seed)-1]
	// FINGERPRINT, CHECKED IN CODE RATHER THAN PROMISED IN A COMMENT.
	// A translation cache is keyed by encoder fingerprint and cleared
	// WHOLESALE on mismatch, so rows rendered for another dialect are a lie
	// that would outlive the clearing of the durable log. Every donated row
	// must carry the SAME fingerprint, and that one is then verified against
	// the log itself by the seam probe below. Fig IR rows carry none, where
	// this is a comparison of empty strings and costs a pass.
	for i := range seed {
		if seed[i].Fingerprint != last.Fingerprint {
			return newWindowedLog(inner, window, budget, inflation, sizeOf)
		}
	}
	probe := inner.ReadFrom(last.FigaroLT, 1)
	if len(probe) != 1 || probe[0].FigaroLT != last.FigaroLT ||
		probe[0].Fingerprint != last.Fingerprint ||
		!reflect.DeepEqual(probe[0].Payload, last.Payload) {
		// The seed does not describe this log's history. Decode instead.
		return newWindowedLog(inner, window, budget, inflation, sizeOf)
	}
	if inflation < 1 {
		inflation = 1
	}
	c := &cachedLog[T]{inner: inner, window: window, budget: budget, sizeOf: sizeOf}
	own := inner.ReadFrom(last.FigaroLT+1, 0)
	v := &logView[T]{rows: make([]Entry[T], 0, len(seed)+len(own))}
	v.rows = append(v.rows, seed...) // shallow: shares every payload string
	v.rows = append(v.rows, own...)
	if total := inner.Len(); total > len(v.rows) {
		v.trimmed = total - len(v.rows)
	}
	for _, e := range v.rows {
		v.bytes += c.size(e)
	}
	c.evictWindow(v, 0)
	c.view.Store(v)
	return c
}

func ascending[T any](rows []Entry[T]) bool {
	for i := 1; i < len(rows); i++ {
		if rows[i].FigaroLT <= rows[i-1].FigaroLT {
			return false
		}
	}
	return true
}
