package render

import "sync"

// Prose is a pure but expensive function: rendering one markdown block through
// glamour is ~1 ms and ~9,000 allocations, and figaro re-renders the same block
// constantly — every re-entry into the transcript re-renders the whole retained
// window, and the live open message re-renders all of its nodes on every tick
// even though only the last one is growing. proseCache turns those repeats into
// a map lookup.
//
// The eviction policy is generational rather than an LRU: entries accumulate in
// hot, and once hot has taken proseCacheBudget bytes it demotes to cold and a
// fresh hot starts. A lookup that hits cold is promoted back into hot, so the
// working set (the retained window) survives rotations and anything stale is
// dropped wholesale on the next one. Retained bytes are bounded by roughly
// 2 x budget, with no per-entry bookkeeping.
const proseCacheBudget = 4 << 20

type proseKey struct {
	width int
	md    string
}

var (
	proseMu    sync.Mutex
	proseHot   = map[proseKey][]string{}
	proseCold  = map[proseKey][]string{}
	proseBytes int
)

func proseEntryBytes(key proseKey, rows []string) int {
	n := len(key.md) + 16*len(rows)
	for _, r := range rows {
		n += len(r)
	}
	return n
}

// lookupProse returns a private copy of the cached rows for (md, width).
// Callers own the slice they get back (renderNodeList and friends rewrite rows
// in place), so the cached slice itself is never handed out.
func lookupProse(md string, width int) ([]string, bool) {
	key := proseKey{width: width, md: md}
	proseMu.Lock()
	defer proseMu.Unlock()
	rows, ok := proseHot[key]
	if !ok {
		if rows, ok = proseCold[key]; !ok {
			return nil, false
		}
		delete(proseCold, key) // promote: the working set outlives a rotation
		proseHot[key] = rows
		proseBytes += proseEntryBytes(key, rows)
	}
	return append([]string(nil), rows...), true
}

func storeProse(md string, width int, rows []string) {
	key := proseKey{width: width, md: md}
	proseMu.Lock()
	defer proseMu.Unlock()
	if proseBytes > proseCacheBudget {
		proseCold, proseHot, proseBytes = proseHot, map[proseKey][]string{}, 0
	}
	proseHot[key] = rows
	proseBytes += proseEntryBytes(key, rows)
}

// resetProseCache is for tests: cache state must not leak between cases.
func resetProseCache() {
	proseMu.Lock()
	defer proseMu.Unlock()
	proseHot, proseCold, proseBytes = map[proseKey][]string{}, map[proseKey][]string{}, 0
}

// proseCacheStats is for tests: entries in each generation and hot bytes.
func proseCacheStats() (hot, cold, bytes int) {
	proseMu.Lock()
	defer proseMu.Unlock()
	return len(proseHot), len(proseCold), proseBytes
}
