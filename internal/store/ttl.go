package store

// system.ttl: a node states its own lifetime on its board, and the daemon's
// sweep removes it once that lifetime is spent.
//
// The deadline lives in the _meta sidecar, never on the board, for one
// measured reason: the sweep must find what is due WITHOUT opening nodes. A
// node open materialises every channel the node owns (~8 logs, ~5 segment
// recoveries), so a reaper that read boards would cost more per tick than the
// whole boot walk. Sidecars are a flat directory of small JSON files: 738 of
// them read in 14 ms with zero node opens.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// SystemTTLKey is the board key that gives a node a lifetime. It is read from
// an aria's own board and from an unbound form's state alike -- both are
// boards, and b.form refuses only librettos, whose lifetime is a refcount and
// never a clock.
const SystemTTLKey = "system.ttl"

// TTLEntry is one node with a lifetime, as the reaper and the doctor see it.
type TTLEntry struct {
	ID          string
	TTL         time.Duration
	CreatedAtMS int64
	DeadlineMS  int64
}

// Expired reports whether this entry is due at nowMS.
func (e TTLEntry) Expired(nowMS int64) bool { return e.DeadlineMS <= nowMS }

// ParseTTL reads a lifetime. Go's duration syntax, plus the d and w suffixes a
// retention policy is actually written in ("30d" is the unit a human reaches
// for; time.ParseDuration stops at hours). An empty value, or a duration of
// zero or less, means no lifetime.
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	mult := time.Duration(0)
	switch {
	case strings.HasSuffix(s, "d"):
		mult = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		mult = 7 * 24 * time.Hour
	}
	if mult > 0 {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s[:len(s)-1], " "), 64)
		if err != nil {
			return 0, fmt.Errorf("ttl %q: %w", s, err)
		}
		return time.Duration(n * float64(mult)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("ttl %q: %w", s, err)
	}
	return d, nil
}

// ttlOf reads the ttl a patch states, and whether the patch speaks about it at
// all. A removal clears the lifetime.
func ttlOf(patch message.Patch) (raw string, spoke bool) {
	if v, ok := patch.Set[SystemTTLKey]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			// A non-string value is a mistake, not a lifetime. Clearing is
			// the safe reading: a malformed ttl must never delete anything.
			return "", true
		}
		return s, true
	}
	for _, k := range patch.Remove {
		if k == SystemTTLKey {
			return "", true
		}
	}
	return "", false
}

// ttlCommitted mirrors a board's stated lifetime into the sidecar, stamping a
// creation time if the node has none. 117 arias and every one of 345 forms
// predate created_at_ms, so "first seen is now" is the stamp they get -- taken
// at the moment the ttl is set, which is the only moment anybody asked.
func (b *XwalBackend) ttlCommitted(id string, patch message.Patch) {
	raw, spoke := ttlOf(patch)
	if !spoke {
		return
	}
	ttl, err := ParseTTL(raw)
	if err != nil {
		slog.Warn("system.ttl ignored", "node", id, "value", raw, "err", err)
		return
	}
	var meta AriaMeta
	if err := b.UpdateMeta(id, func(m *AriaMeta) {
		if m.CreatedAtMS == 0 {
			m.CreatedAtMS = time.Now().UnixMilli()
		}
		m.TTLMS = ttl.Milliseconds()
		meta = *m
	}); err != nil {
		slog.Warn("system.ttl: sidecar unwritable", "node", id, "err", err)
		return
	}
	b.ttlNoted(id, &meta)
	if ttl > 0 {
		slog.Info("node lifetime set", "node", id, "ttl", ttl,
			"expires", time.UnixMilli(meta.CreatedAtMS+ttl.Milliseconds()))
	} else {
		slog.Info("node lifetime cleared", "node", id)
	}
}

// ttlNoted keeps the in-memory deadline set current. It holds only the nodes
// that carry a lifetime -- an opt-in key, so the map is empty on a store
// nobody has asked to expire anything on.
func (b *XwalBackend) ttlNoted(id string, meta *AriaMeta) {
	b.ttlMu.Lock()
	defer b.ttlMu.Unlock()
	if b.ttl == nil {
		return // not seeded yet; the scan will find it
	}
	if meta == nil || meta.TTLMS <= 0 {
		delete(b.ttl, id)
		return
	}
	b.ttl[id] = TTLEntry{
		ID:          id,
		TTL:         time.Duration(meta.TTLMS) * time.Millisecond,
		CreatedAtMS: meta.CreatedAtMS,
		DeadlineMS:  meta.CreatedAtMS + meta.TTLMS,
	}
}

// TTLEntries is every node with a lifetime. The first call scans the sidecar
// directory once (a flat directory of small files, no node opens); after that
// it is a map read, kept current by every board commit that names the key.
func (b *XwalBackend) TTLEntries() []TTLEntry {
	b.ttlMu.Lock()
	if b.ttl == nil {
		b.ttl = b.ttlScan()
	}
	out := make([]TTLEntry, 0, len(b.ttl))
	for _, e := range b.ttl {
		out = append(out, e)
	}
	b.ttlMu.Unlock()
	return out
}

// TTLDue is the entries whose lifetime is spent at nowMS.
func (b *XwalBackend) TTLDue(nowMS int64) []TTLEntry {
	var due []TTLEntry
	for _, e := range b.TTLEntries() {
		if e.Expired(nowMS) {
			due = append(due, e)
		}
	}
	return due
}

// TTLForget drops a node from the deadline set: what the reaper deleted, and
// what a delete by any other hand took with it.
func (b *XwalBackend) TTLForget(ids ...string) {
	b.ttlMu.Lock()
	defer b.ttlMu.Unlock()
	for _, id := range ids {
		delete(b.ttl, id)
	}
}

// ttlScan reads the sidecar directory. Caller holds ttlMu.
func (b *XwalBackend) ttlScan() map[string]TTLEntry {
	return ScanTTL(b.root)
}

// ScanTTL reads every lifetime an aria root states, from the sidecars alone:
// no store is opened, no lock is taken, nothing is woken. That is what lets
// the report run against a LIVE daemon's store -- and the sidecar is the
// authority in any case, because it is written before the daemon notes it.
func ScanTTL(root string) map[string]TTLEntry {
	found := map[string]TTLEntry{}
	dir := filepath.Join(root, "_meta")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("ttl scan", "dir", dir, "err", err)
		}
		return found
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		meta, err := readJSON[AriaMeta](filepath.Join(dir, e.Name()))
		if err != nil || meta == nil || meta.TTLMS <= 0 {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		found[id] = TTLEntry{
			ID:          id,
			TTL:         time.Duration(meta.TTLMS) * time.Millisecond,
			CreatedAtMS: meta.CreatedAtMS,
			DeadlineMS:  meta.CreatedAtMS + meta.TTLMS,
		}
	}
	return found
}
