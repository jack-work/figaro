package store

import (
	"testing"
	"time"
)

// The keepalive is the reaper's mercy: it must reach the sidecar the sweep reads.
func TestKeepaliveMirrorsIntoTheIndex(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemTTLKey, "1h")
	setBoardKey(t, b, id, SystemKeepaliveKey, "2h")

	e := ScanTTL(b.root)[id]
	if e.Keepalive == nil || *e.Keepalive != 2*time.Hour {
		t.Fatalf("keepalive %v, want 2h", e.Keepalive)
	}
	setBoardKey(t, b, id, SystemKeepaliveKey, "0")
	e = ScanTTL(b.root)[id]
	if e.Keepalive == nil || *e.Keepalive != 0 {
		t.Fatalf("keepalive %v after \"0\", want an explicit zero (no mercy), not the default", e.Keepalive)
	}
	removeBoardKey(t, b, id, SystemKeepaliveKey)
	if e = ScanTTL(b.root)[id]; e.Keepalive != nil {
		t.Fatalf("keepalive %v after removal, want nil (the daemon default)", *e.Keepalive)
	}
}

func TestRetiredMirrorsAndClears(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemRetiredKey, "2026-10-21T06:00:00Z")
	meta, _ := b.Meta(id)
	if meta == nil || meta.RetiredAtMS == 0 {
		t.Fatal("system.retired did not reach the sidecar; the prompt refusal reads it there")
	}
	setBoardKey(t, b, id, SystemRetiredKey, "false")
	if meta, _ = b.Meta(id); meta.RetiredAtMS != 0 {
		t.Fatalf("retired_at_ms %d after \"false\", want 0", meta.RetiredAtMS)
	}
	setBoardKey(t, b, id, SystemRetiredKey, "yes")
	removeBoardKey(t, b, id, SystemRetiredKey)
	if meta, _ = b.Meta(id); meta.RetiredAtMS != 0 {
		t.Fatalf("retired_at_ms %d after removal, want 0", meta.RetiredAtMS)
	}
}

func TestReapsAtHonoursKeepalive(t *testing.T) {
	day := 24 * time.Hour
	hour := time.Hour.Milliseconds()
	zero := time.Duration(0)
	two := 2 * time.Hour
	cases := []struct {
		name string
		e    TTLEntry
		last int64
		want int64
	}{
		{"never touched: the deadline", TTLEntry{DeadlineMS: 100 * hour}, 0, 100 * hour},
		{"touched late: deadline moves to touch+default", TTLEntry{DeadlineMS: 100 * hour}, 99 * hour, 123 * hour},
		{"access stamp counts", TTLEntry{DeadlineMS: 100 * hour, LastAccessMS: 110 * hour}, 99 * hour, 134 * hour},
		{"a write nobody asked for does not extend a stamped node", TTLEntry{DeadlineMS: 100 * hour, LastAccessMS: 90 * hour}, 99 * hour, 114 * hour},
		{"own keepalive wins", TTLEntry{DeadlineMS: 100 * hour, Keepalive: &two}, 99 * hour, 101 * hour},
		{"explicit zero: no mercy", TTLEntry{DeadlineMS: 100 * hour, Keepalive: &zero}, 99 * hour, 100 * hour},
		{"retired: the default does not hold it", TTLEntry{DeadlineMS: 100 * hour, RetiredAtMS: 1}, 99 * hour, 100 * hour},
		{"retired with its own keepalive", TTLEntry{DeadlineMS: 100 * hour, RetiredAtMS: 1, Keepalive: &two}, 99 * hour, 101 * hour},
	}
	for _, c := range cases {
		if got := c.e.ReapsAt(day, c.last); got != c.want {
			t.Errorf("%s: ReapsAt = %dh, want %dh", c.name, got/hour, c.want/hour)
		}
	}
}

// An access costs a sidecar write at most once per granularity, and what was
// written survives a fresh scan, as after a restart.
func TestTouchIsThrottledAndDurable(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemTTLKey, "1h")
	t0 := time.Now()
	b.Touch(id, t0)
	if got := ScanTTL(b.root)[id].LastAccessMS; got != t0.UnixMilli() {
		t.Fatalf("last access %d, want %d", got, t0.UnixMilli())
	}
	b.Touch(id, t0.Add(time.Minute))
	if got := ScanTTL(b.root)[id].LastAccessMS; got != t0.UnixMilli() {
		t.Fatalf("a touch inside the granularity rewrote the sidecar: %d", got)
	}
	if got := b.TTLEntries()[0].LastAccessMS; got != t0.Add(time.Minute).UnixMilli() {
		t.Fatalf("the in-memory stamp is %d; a touch inside the granularity must still count in memory", got)
	}
	if got := b.LastAccess(id); got != t0.Add(time.Minute).UnixMilli() {
		t.Fatalf("LastAccess %d, want the unthrottled stamp", got)
	}
	later := t0.Add(touchGranularity + time.Second)
	b.Touch(id, later)
	if got := ScanTTL(b.root)[id].LastAccessMS; got != later.UnixMilli() {
		t.Fatalf("last access %d, want %d", got, later.UnixMilli())
	}
	if got := b.TTLEntries()[0].LastAccessMS; got != later.UnixMilli() {
		t.Fatalf("the in-memory index kept %d; the sweep reads it, not the file", got)
	}
}

// A fork is how a retired conversation continues: it must take prompts.
func TestForkOfARetiredAriaIsNotRetired(t *testing.T) {
	b := newTTLBackend(t)
	id := mustNewAria(t, b)
	setBoardKey(t, b, id, SystemRetiredKey, "2026-10-21T06:00:00Z")
	child, _, err := b.ForkWith(id, 0, setPatch(map[string]string{"mantra": "fork"}))
	if err != nil {
		t.Fatal(err)
	}
	if meta, _ := b.Meta(child); meta != nil && meta.RetiredAtMS != 0 {
		t.Fatalf("fork %s is retired; a fork of a retired aria must take prompts", child)
	}
	snap, err := b.FormState(child)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup(SystemRetiredKey); v != nil {
		t.Errorf("fork's board still says %s=%q", SystemRetiredKey, *v)
	}
	if meta, _ := b.Meta(id); meta == nil || meta.RetiredAtMS == 0 {
		t.Error("forking unretired the parent")
	}
}
