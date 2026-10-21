package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// A row hidden as expired while its keepalive still holds it would hide an
// aria the daemon is keeping: the very confusion keepalive exists to end.
func TestExpiredWaitsForTheKeepalive(t *testing.T) {
	now := time.Now()
	f := rpc.FigaroInfoResponse{
		ID: "kept", State: "dormant",
		ExpiresAt: now.Add(-time.Hour).UnixMilli(),
		ReapsAt:   now.Add(5 * time.Hour).UnixMilli(),
	}
	if expired(f, now.UnixMilli()) {
		t.Fatal("a row the keepalive holds was treated as expired")
	}
	f.ReapsAt = now.Add(-time.Minute).UnixMilli()
	if !expired(f, now.UnixMilli()) {
		t.Fatal("a row past its reap time was not treated as expired")
	}
	old := rpc.FigaroInfoResponse{ID: "old", State: "dormant", ExpiresAt: now.Add(-time.Hour).UnixMilli()}
	if !expired(old, now.UnixMilli()) {
		t.Fatal("a daemon without keepalive reports only expires_at; it must still be honoured")
	}
}

func TestLifeNoteMarksRowsNearReaping(t *testing.T) {
	now := time.Now()
	day := (24 * time.Hour).Milliseconds()
	cases := []struct {
		name string
		f    rpc.FigaroInfoResponse
		want string
	}{
		{"no lifetime", rpc.FigaroInfoResponse{}, ""},
		{"far off", rpc.FigaroInfoResponse{ReapsAt: now.Add(20 * time.Hour).UnixMilli(), KeepaliveMS: day}, ""},
		{"inside a quarter of the keepalive", rpc.FigaroInfoResponse{ReapsAt: now.Add(3 * time.Hour).UnixMilli(), KeepaliveMS: day}, "[reaps in 3h]"},
		{"minutes", rpc.FigaroInfoResponse{ReapsAt: now.Add(40 * time.Minute).UnixMilli(), KeepaliveMS: day}, "[reaps in 40m]"},
		{"due and held", rpc.FigaroInfoResponse{ReapsAt: now.Add(-time.Minute).UnixMilli()}, "[reaping]"},
		{"retired", rpc.FigaroInfoResponse{RetiredAt: now.UnixMilli()}, "[retired]"},
		{"retired and going", rpc.FigaroInfoResponse{RetiredAt: 1, ReapsAt: now.Add(30 * time.Minute).UnixMilli()}, "[retired, reaps in 30m]"},
	}
	for _, c := range cases {
		if got := lifeNote(c.f, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWithLifeNoteKeepsTheNoteWhole(t *testing.T) {
	now := time.Now()
	f := rpc.FigaroInfoResponse{RetiredAt: 1}
	got := withLifeNote(strings.Repeat("mantra ", 20), 44, f, now)
	if !strings.HasSuffix(got, " [retired]") {
		t.Fatalf("%q lost its note to truncation", got)
	}
	if n := len([]rune(got)); n > 44 {
		t.Fatalf("%q is %d runes, want <= 44", got, n)
	}
}

func TestRetirePatchStampsAndUndoes(t *testing.T) {
	now := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	d, err := parseDress("", "system.ttl=1h,mantra=epitaph", "")
	if err != nil {
		t.Fatal(err)
	}
	p := retirePatch(d, false, now)
	e, ok := p.Entry(store.SystemRetiredKey)
	if !ok || string(e.New) != `"2026-10-21T06:00:00Z"` {
		t.Fatalf("retired entry %+v, want the stamp", e)
	}
	if _, ok := p.Entry("system.ttl"); !ok {
		t.Fatal("-S keys must ride the same write as the retirement")
	}
	undo := retirePatch(d, true, now)
	for _, e := range undo.Entries() {
		if e.Key == store.SystemRetiredKey && !e.IsRemoval() {
			t.Fatal("--undo set the key instead of removing it")
		}
	}
}

func TestTTLFromNowCountsTheAge(t *testing.T) {
	now := time.Now()
	created := now.Add(-72 * time.Hour).UnixMilli()
	got, err := store.ParseTTL(ttlFromNow(created, time.Hour, now))
	if err != nil {
		t.Fatal(err)
	}
	if got != 73*time.Hour {
		t.Fatalf("ttl %v, want 73h: three days old plus one hour from now", got)
	}
}
