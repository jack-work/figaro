package angelus

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

func TestANameIsRefusedWhileAFormCarryingItCannotBeRead(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(map[bool]string{false: "scan", true: "index"}[seeded], func(t *testing.T) {
			fault := NewFormFault(store.NewTestBackend(t))
			r := &nameRig{t: t, a: &Angelus{Registry: NewRegistry(), Backend: fault, Hubs: newHubs()}}
			role := r.role("shared", r.aria())
			r.role("shared", "")
			if seeded {
				fault.SeedRoles(context.Background(), 0)
			}
			r.serve()
			r.resolves(r.owner, "@shared", role)

			fault.Break(role)
			got, err := r.h.canonicalID(r.owner, "@shared")
			if err == nil || !strings.Contains(err.Error(), "cannot be read") {
				t.Fatalf("@shared with the role unreadable = %q, %v; want refused, never the other form", got, err)
			}
			fault.Heal()
			r.resolves(r.owner, "@shared", role)
		})
	}
}
