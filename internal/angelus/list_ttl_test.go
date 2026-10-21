package angelus

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type ttlListBackend struct {
	store.Backend // never called: fillTTL touches only the two below
	entries       []store.TTLEntry
	lastTS        map[string]int64
}

func (f *ttlListBackend) TTLEntries() []store.TTLEntry { return f.entries }
func (f *ttlListBackend) LastTS(id string) int64       { return f.lastTS[id] }

// The row says when the sweep may take it, not just the birth deadline.
func TestFillTTLReportsTheKeepaliveReapTime(t *testing.T) {
	created := time.Now().Add(-90 * time.Minute)
	written := time.Now().Add(-10 * time.Minute)
	be := &ttlListBackend{
		entries: []store.TTLEntry{{
			ID: "used", TTL: time.Hour,
			CreatedAtMS: created.UnixMilli(), DeadlineMS: created.Add(time.Hour).UnixMilli(),
		}},
		lastTS: map[string]int64{"used": written.UnixMilli()},
	}
	h := &handlers{angelus: &Angelus{Backend: be}}
	rows := []rpc.FigaroInfoResponse{{ID: "used"}}
	h.fillTTL(rows)
	if want := written.Add(24 * time.Hour).UnixMilli(); rows[0].ReapsAt != want {
		t.Errorf("reaps_at = %v, want %v (last write + the default 1d keepalive)",
			time.UnixMilli(rows[0].ReapsAt), time.UnixMilli(want))
	}
	if rows[0].KeepaliveMS != (24 * time.Hour).Milliseconds() {
		t.Errorf("keepalive_ms = %d, want one day", rows[0].KeepaliveMS)
	}
}

type backendWithoutLifetimes struct{ store.Backend }

func TestFillTTLStampsTheDeadline(t *testing.T) {
	created := time.Now().Add(-90 * time.Minute)
	be := &ttlListBackend{entries: []store.TTLEntry{{
		ID:          "scratch",
		TTL:         time.Hour,
		CreatedAtMS: created.UnixMilli(),
		DeadlineMS:  created.Add(time.Hour).UnixMilli(),
	}}}
	h := &handlers{angelus: &Angelus{Backend: be}}
	rows := []rpc.FigaroInfoResponse{{ID: "scratch"}, {ID: "keeper"}}
	h.fillTTL(rows)

	if rows[0].TTLMS != time.Hour.Milliseconds() {
		t.Errorf("ttl_ms = %d, want %d", rows[0].TTLMS, time.Hour.Milliseconds())
	}
	if want := created.Add(time.Hour).UnixMilli(); rows[0].ExpiresAt != want {
		t.Errorf("expires_at = %d, want %d", rows[0].ExpiresAt, want)
	}
	// The row nobody gave a lifetime stays at zero, or the client reads it as expired at the epoch.
	if rows[1].TTLMS != 0 || rows[1].ExpiresAt != 0 {
		t.Errorf("row without a lifetime = {%d, %d}, want zeroes",
			rows[1].TTLMS, rows[1].ExpiresAt)
	}
}

func TestFillTTLIgnoresABackendWithoutLifetimes(t *testing.T) {
	h := &handlers{angelus: &Angelus{Backend: &backendWithoutLifetimes{}}}
	rows := []rpc.FigaroInfoResponse{{ID: "a", TTLMS: 0}}
	h.fillTTL(rows)
	if rows[0].ExpiresAt != 0 {
		t.Errorf("expires_at = %d, want 0", rows[0].ExpiresAt)
	}
}
