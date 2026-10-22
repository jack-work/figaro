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

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// SystemTTLKey is read from an aria's board and an unbound form's state alike;
// b.form refuses only librettos, whose lifetime is a refcount, never a clock.
const SystemTTLKey = "system.ttl"

const SystemKeepaliveKey = "system.keepalive"

const SystemRetiredKey = "system.retired"

type TTLEntry struct {
	ID           string
	TTL          time.Duration
	CreatedAtMS  int64
	DeadlineMS   int64
	Keepalive    *time.Duration
	RetiredAtMS  int64
	LastAccessMS int64
	ReapedAtMS   int64
}

func (e TTLEntry) Expired(nowMS int64) bool { return e.DeadlineMS <= nowMS }

func (e TTLEntry) KeepaliveOr(def time.Duration) time.Duration {
	if e.Keepalive != nil {
		return max(*e.Keepalive, 0)
	}
	if e.RetiredAtMS > 0 {
		return 0
	}
	return max(def, 0)
}

func KeepaliveFor(meta *AriaMeta, def time.Duration) time.Duration {
	if meta == nil {
		return max(def, 0)
	}
	e := TTLEntry{RetiredAtMS: meta.RetiredAtMS}
	if meta.KeepaliveMS != nil {
		ka := time.Duration(*meta.KeepaliveMS) * time.Millisecond
		e.Keepalive = &ka
	}
	return e.KeepaliveOr(def)
}

func (e TTLEntry) ReapsAt(def time.Duration, lastWriteMS int64) int64 {
	return max(e.DeadlineMS, LastUse(e.LastAccessMS, lastWriteMS)+e.KeepaliveOr(def).Milliseconds())
}

func LastUse(accessMS, lastWriteMS int64) int64 {
	if accessMS > 0 {
		return accessMS
	}
	return lastWriteMS
}

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
	return stringKeyOf(patch, SystemTTLKey)
}

func stringKeyOf(patch message.Patch, key string) (raw string, spoke bool) {
	if e, ok := patch.Entry(key); ok && !e.IsRemoval() {
		return lifetimeString(e.New), true
	}
	for _, ent := range patch.Entries() {
		if ent.IsRemoval() && ent.Key == key {
			return "", true
		}
	}
	return "", false
}

func lifetimeString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

type lifetimeChange struct {
	ttl          *time.Duration
	keepalive    *time.Duration
	keepaliveSet bool
	retired      *bool
	reapedRoot   *string
}

func lifetimeOf(id string, patch message.Patch) (lifetimeChange, bool) {
	return lifetimeFrom(id, func(key string) (string, bool) { return stringKeyOf(patch, key) })
}

func lifetimeOfBoard(id string, board form.Snapshot) lifetimeChange {
	c, _ := lifetimeFrom(id, func(key string) (string, bool) {
		raw, ok := board.Get(key)
		if !ok {
			return "", true
		}
		return lifetimeString(raw), true
	})
	return c
}

func lifetimeFrom(id string, valueOf func(key string) (string, bool)) (lifetimeChange, bool) {
	var c lifetimeChange
	if raw, spoke := valueOf(SystemTTLKey); spoke {
		if ttl, err := ParseTTL(raw); err != nil {
			slog.Warn("system.ttl ignored", "node", id, "value", raw, "err", err)
		} else {
			c.ttl = &ttl
		}
	}
	if raw, spoke := valueOf(SystemKeepaliveKey); spoke {
		if ka, err := ParseTTL(raw); err != nil {
			slog.Warn("system.keepalive ignored", "node", id, "value", raw, "err", err)
		} else {
			c.keepalive, c.keepaliveSet = &ka, strings.TrimSpace(raw) != ""
		}
	}
	if raw, spoke := valueOf(SystemRetiredKey); spoke {
		on := retiredValue(raw)
		c.retired = &on
	}
	if raw, spoke := valueOf(SystemReapedKey); spoke {
		root := strings.TrimSpace(raw)
		c.reapedRoot = &root
	}
	return c, c.ttl != nil || c.keepalive != nil || c.retired != nil || c.reapedRoot != nil
}

func retiredValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func (c lifetimeChange) applyTo(m *AriaMeta, now int64) {
	if c.ttl != nil {
		m.TTLMS = c.ttl.Milliseconds()
	}
	if c.keepalive != nil {
		if c.keepaliveSet {
			ms := c.keepalive.Milliseconds()
			m.KeepaliveMS = &ms
		} else {
			m.KeepaliveMS = nil
		}
	}
	if c.retired != nil {
		switch {
		case *c.retired && m.RetiredAtMS == 0:
			m.RetiredAtMS = now
		case !*c.retired:
			m.RetiredAtMS = 0
		}
	}
	if c.reapedRoot != nil {
		switch {
		case *c.reapedRoot == "":
			m.ReapedAtMS, m.ReapedRoot = 0, ""
		case m.ReapedAtMS == 0 || m.ReapedRoot != *c.reapedRoot:
			m.ReapedAtMS, m.ReapedRoot = now, *c.reapedRoot
		}
	}
}

func sameLifetime(a, b *AriaMeta) bool {
	ka, kb := a.KeepaliveMS, b.KeepaliveMS
	return a.TTLMS == b.TTLMS && a.RetiredAtMS == b.RetiredAtMS &&
		a.ReapedAtMS == b.ReapedAtMS && a.ReapedRoot == b.ReapedRoot &&
		(ka == nil) == (kb == nil) && (ka == nil || *ka == *kb)
}

func (b *XwalBackend) ttlCommitted(id string, patch message.Patch) {
	if c, spoke := lifetimeOf(id, patch); spoke {
		b.mirrorLifetime(id, c)
	}
}

func (b *XwalBackend) mirrorBoardLifetime(id string, board form.Snapshot) {
	c := lifetimeOfBoard(id, board)
	cur, _ := b.Meta(id)
	if cur == nil {
		cur = &AriaMeta{}
	}
	probe := *cur
	c.applyTo(&probe, time.Now().UnixMilli())
	if sameLifetime(&probe, cur) {
		return
	}
	b.mirrorLifetime(id, c)
}

func (b *XwalBackend) lifetimeAtBirth(id string) {
	f, err := b.form(id)
	if err != nil {
		slog.Warn("lifetime: board unreadable at birth", "node", id, "err", err)
		return
	}
	b.mirrorBoardLifetime(id, f.Read().Snapshot)
}

func (b *XwalBackend) mirrorLifetime(id string, c lifetimeChange) {
	now := time.Now().UnixMilli()
	var meta AriaMeta
	if err := b.UpdateMeta(id, func(m *AriaMeta) {
		if m.CreatedAtMS == 0 {
			m.CreatedAtMS = now
		}
		c.applyTo(m, now)
		meta = *m
	}); err != nil {
		slog.Warn("lifetime: sidecar unwritable", "node", id, "err", err)
		return
	}
	b.ttlNoted(id, &meta)
	if c.ttl != nil {
		if *c.ttl > 0 {
			slog.Info("node lifetime set", "node", id, "ttl", *c.ttl,
				"expires", time.UnixMilli(meta.CreatedAtMS+meta.TTLMS))
		} else {
			slog.Info("node lifetime cleared", "node", id)
		}
	}
	if c.retired != nil {
		slog.Info("node retirement changed", "node", id, "retired", *c.retired)
	}
	if c.reapedRoot != nil {
		slog.Info("node reaped state changed", "node", id, "root", *c.reapedRoot)
	}
}

// ttlNoted holds only the nodes that carry a lifetime.
func (b *XwalBackend) ttlNoted(id string, meta *AriaMeta) {
	b.ttlMu.Lock()
	if b.ttl == nil {
		b.ttlMu.Unlock()
		return
	}
	if e, ok := ttlEntryOf(id, meta); ok {
		e.LastAccessMS = max(e.LastAccessMS, b.access[id])
		b.ttl[id] = e
	} else {
		delete(b.ttl, id)
	}
	changed := b.reapedNotedLocked(id, meta)
	b.ttlMu.Unlock()
	if changed {
		b.reapedChanged()
	}
}

func ttlEntryOf(id string, meta *AriaMeta) (TTLEntry, bool) {
	if meta == nil || meta.TTLMS <= 0 {
		return TTLEntry{}, false
	}
	e := TTLEntry{
		ID:           id,
		TTL:          time.Duration(meta.TTLMS) * time.Millisecond,
		CreatedAtMS:  meta.CreatedAtMS,
		DeadlineMS:   meta.CreatedAtMS + meta.TTLMS,
		RetiredAtMS:  meta.RetiredAtMS,
		LastAccessMS: meta.LastAccessMS,
		ReapedAtMS:   meta.ReapedAtMS,
	}
	if meta.KeepaliveMS != nil {
		ka := time.Duration(*meta.KeepaliveMS) * time.Millisecond
		e.Keepalive = &ka
	}
	return e, true
}

const touchGranularity = 10 * time.Minute

func (b *XwalBackend) Touch(id string, at time.Time) {
	meta, err := b.Meta(id)
	if err != nil || meta == nil {
		return
	}
	ms := at.UnixMilli()
	b.ttlMu.Lock()
	if b.access == nil {
		b.access = map[string]int64{}
	}
	if ms > b.access[id] {
		b.access[id] = ms
	}
	if e, ok := b.ttl[id]; ok && ms > e.LastAccessMS {
		e.LastAccessMS = ms
		b.ttl[id] = e
	}
	b.ttlMu.Unlock()
	if ms-meta.LastAccessMS < touchGranularity.Milliseconds() {
		return
	}
	if err := b.UpdateMeta(id, func(m *AriaMeta) {
		if ms > m.LastAccessMS {
			m.LastAccessMS = ms
		}
	}); err != nil {
		slog.Warn("access stamp: sidecar unwritable", "node", id, "err", err)
	}
}

// TTLEntries scans the sidecar directory once; after that it is a map read
// kept current by every board commit that names the key.
func (b *XwalBackend) seedLifetimesLocked() {
	if b.ttl != nil {
		return
	}
	b.ttl, b.reaped = scanLifetimes(b.root)
	for id, e := range b.ttl {
		e.LastAccessMS = max(e.LastAccessMS, b.access[id])
		b.ttl[id] = e
	}
}

func (b *XwalBackend) TTLEntries() []TTLEntry {
	b.ttlMu.Lock()
	b.seedLifetimesLocked()
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

// ScanTTL reads the sidecars alone: no store opened, no lock taken, nothing
// woken, so it may run against a live daemon's store.
func ScanTTL(root string) map[string]TTLEntry {
	found, _ := scanLifetimes(root)
	return found
}

func scanLifetimes(root string) (map[string]TTLEntry, map[string]ReapedEntry) {
	found := map[string]TTLEntry{}
	reaped := map[string]ReapedEntry{}
	dir := filepath.Join(root, "_meta")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("ttl scan", "dir", dir, "err", err)
		}
		return found, reaped
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		meta, err := readJSON[AriaMeta](filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if entry, ok := ttlEntryOf(id, meta); ok {
			found[id] = entry
		}
		if entry, ok := reapedEntryOf(id, meta); ok {
			reaped[id] = entry
		}
	}
	return found, reaped
}

func (b *XwalBackend) LastAccess(id string) int64 {
	b.ttlMu.Lock()
	ms := b.access[id]
	b.ttlMu.Unlock()
	if meta, _ := b.Meta(id); meta != nil {
		ms = max(ms, meta.LastAccessMS)
	}
	return ms
}
