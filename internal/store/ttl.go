package store

// The deadline lives in the _meta sidecar, never on the board: the sweep must
// find what is due without opening nodes.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
)

// SystemTTLKey is read from an aria's board and an unbound form's state alike;
// b.form refuses only librettos, whose lifetime is a refcount, never a clock.
const SystemTTLKey = "system.ttl"

type TTLEntry struct {
	ID          string
	TTL         time.Duration
	CreatedAtMS int64
	DeadlineMS  int64
}

func (e TTLEntry) Expired(nowMS int64) bool { return e.DeadlineMS <= nowMS }

// ParseTTL accepts Go duration syntax plus the d and w suffixes. Empty, zero
// or negative means no lifetime.
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

// ttlOf: spoke reports whether the patch names the key at all; a removal clears the lifetime.
func ttlOf(patch message.Patch) (raw string, spoke bool) {
	if e, ok := patch.Entry(SystemTTLKey); ok && !e.IsRemoval() {
		v := e.New
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			// A malformed ttl must never delete anything: clear it.
			return "", true
		}
		return s, true
	}
	for _, ent := range patch.Entries() {
		if !ent.IsRemoval() {
			continue
		}
		k := ent.Key
		if k == SystemTTLKey {
			return "", true
		}
	}
	return "", false
}

// ttlCommitted mirrors the board's lifetime into the sidecar; a node without
// created_at_ms is stamped now, the only moment anybody asked.
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

// ttlNoted holds only the nodes that carry a lifetime.
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

// TTLEntries scans the sidecar directory once; after that it is a map read
// kept current by every board commit that names the key.
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

func (b *XwalBackend) TTLDue(nowMS int64) []TTLEntry {
	var due []TTLEntry
	for _, e := range b.TTLEntries() {
		if e.Expired(nowMS) {
			due = append(due, e)
		}
	}
	return due
}

func (b *XwalBackend) TTLForget(ids ...string) {
	b.ttlMu.Lock()
	defer b.ttlMu.Unlock()
	for _, id := range ids {
		delete(b.ttl, id)
	}
}

// Caller holds ttlMu.
func (b *XwalBackend) ttlScan() map[string]TTLEntry {
	return ScanTTL(b.root)
}

// ScanTTL reads the sidecars alone: no store opened, no lock taken, nothing
// woken, so it may run against a live daemon's store.
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
