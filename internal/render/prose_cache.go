package render

import "sync"

const proseCacheBudget = 4 << 20

type proseKey struct {
	width int
	md    string
}

var (
	proseMu     sync.Mutex
	proseHot    = map[proseKey][]string{}
	proseCold   = map[proseKey][]string{}
	proseBytes  int
	proseLastMD = map[int]string{}
)

func proseEntryBytes(key proseKey, rows []string) int {
	n := len(key.md) + 16*len(rows)
	for _, r := range rows {
		n += len(r)
	}
	return n
}

// lookupProse returns a private copy: callers rewrite rows in place.
func lookupProse(md string, width int) ([]string, bool) {
	key := proseKey{width: width, md: md}
	proseMu.Lock()
	defer proseMu.Unlock()
	rows, ok := proseHot[key]
	if !ok {
		if rows, ok = proseCold[key]; !ok {
			return nil, false
		}
		delete(proseCold, key)
		proseHot[key] = rows
		proseBytes += proseEntryBytes(key, rows)
	}
	return append([]string(nil), rows...), true
}

func storeProse(md string, width int, rows []string) {
	key := proseKey{width: width, md: md}
	proseMu.Lock()
	defer proseMu.Unlock()
	// A streaming node's previous partial is a prefix of this one: drop it.
	if prev, ok := proseLastMD[width]; ok && prev != md && len(prev) < len(md) &&
		md[:len(prev)] == prev {
		stale := proseKey{width: width, md: prev}
		if old, ok := proseHot[stale]; ok {
			proseBytes -= proseEntryBytes(stale, old)
			delete(proseHot, stale)
		}
		delete(proseCold, stale)
	}
	proseLastMD[width] = md
	if proseBytes > proseCacheBudget {
		proseCold, proseHot, proseBytes = proseHot, map[proseKey][]string{}, 0
	}
	proseHot[key] = rows
	proseBytes += proseEntryBytes(key, rows)
}

func resetProseCache() {
	proseMu.Lock()
	defer proseMu.Unlock()
	proseHot, proseCold, proseBytes = map[proseKey][]string{}, map[proseKey][]string{}, 0
	proseLastMD = map[int]string{}
}

func proseCacheStats() (hot, cold, bytes int) {
	proseMu.Lock()
	defer proseMu.Unlock()
	return len(proseHot), len(proseCold), proseBytes
}
