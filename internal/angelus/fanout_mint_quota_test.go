package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func TestALabeledMintIsChargedToTheMinter(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	giveMint(t, r, r.x)
	if _, err := r.backend.ApplyForm(r.x, *heldPatch(t, map[string]any{form.FanoutQuotaKey: 1})); err != nil {
		t.Fatal(err)
	}
	out, err := r.createAs(t, r.x)
	if err != nil {
		t.Fatalf("a licensed minter with budget was refused: %v", err)
	}
	if n, _ := r.backend.Node(out.FigaroID); n.Present != r.x {
		t.Fatalf("the minted figaro is drawn under %q, not its minter %s", n.Present, r.x)
	}
	snap, err := r.backend.FormState(out.FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := snap.Get(form.FanoutQuotaKey); !ok || string(v) != "0" {
		t.Fatalf("the minted figaro's quota = %s (present %v), want 0", v, ok)
	}
	if _, err := r.createAs(t, r.x); err == nil || !strings.Contains(err.Error(), "0 free of quota 1") {
		t.Fatalf("a minter with no budget left minted again: %v", err)
	}
}
