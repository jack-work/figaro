package angelus_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro/wire"
)

func TestMintingALogChargesTheMinter(t *testing.T) {
	r := newLicenseRig(t, defaultPolicy)
	if err := wire.Install(r.backend.Store(), t.TempDir(), wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	giveMint(t, r, r.x)
	if _, err := r.backend.ApplyForm(r.x, *heldPatch(t, map[string]any{form.FanoutQuotaKey: 1})); err != nil {
		t.Fatal(err)
	}
	bind := func() (string, error) {
		raw, _ := json.Marshal(rpc.FormBindRequest{Patch: logBirth(), Cwd: r.cwd})
		out, err := r.h[rpc.MethodFormBind](localOwner(), claimed(t, r.x, raw))
		if err != nil {
			return "", err
		}
		return out.(rpc.FormBindResponse).FigaroID, nil
	}
	id, err := bind()
	if err != nil {
		t.Fatalf("a licensed minter with budget was refused a log: %v", err)
	}
	if n, _ := r.backend.Node(id); n.Present != r.x {
		t.Fatalf("the log is drawn under %q, not its minter %s", n.Present, r.x)
	}
	if _, err := bind(); err == nil || !strings.Contains(err.Error(), "0 free of quota 1") {
		t.Fatalf("a minter with no budget left minted a second log: %v", err)
	}
}
