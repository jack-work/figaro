package angelus

// What a listing says about a node's lifetime. The sweep that acts on it is
// ttl_sweep_test.go; this covers only the fields the wire carries.

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type ttlListBackend struct {
	store.Backend // never called: fillTTL touches only TTLEntries
	entries       []store.TTLEntry
}

func (f *ttlListBackend) TTLEntries() []store.TTLEntry { return f.entries }

// backendWithoutLifetimes has no TTLEntries at all, which is every backend a
// test writes by hand.
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
	// The row nobody gave a lifetime must stay at zero, or the client's
	// filter would read every aria as expired at the epoch.
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
