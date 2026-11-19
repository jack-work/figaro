package angelus_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const layeredOutfitsConfig = "[outfits]\nroots = [\"managed\", \"outfits\"]\n"

func layeredRig(t *testing.T) (r grantRig, managed, user string) {
	t.Helper()
	r = newRigConfigured(t, grantPolicy, layeredOutfitsConfig)
	dir := filepath.Dir(r.policy)
	managed, user = filepath.Join(dir, "managed"), filepath.Join(dir, "outfits")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	return r, managed, user
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheDaemonReportsAnOverlayALockRefused(t *testing.T) {
	r, managed, user := layeredRig(t)
	writeFile(t, filepath.Join(managed, "seat.toml"), "[system]\nprovider = \"mock\"\nmodel = \"managed\"\n")
	writeFile(t, filepath.Join(managed, ".locked"), "seat\n")
	writeFile(t, filepath.Join(user, "seat.toml"), "[system]\nprovider = \"mock\"\nmodel = \"user\"\n")
	want := []rpc.OutfitRefusal{{Name: "seat", Path: filepath.Join(user, "seat.toml"), LockedBy: filepath.Join(managed, ".locked")}}

	out, err := r.call(asOwner(), rpc.MethodConfigure, rpc.ConfigureRequest{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(rpc.ConfigureResponse).Refused; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("refresh refused = %+v, want %+v", got, want)
	}
	out, err = r.call(asOwner(), rpc.MethodOutfits, rpc.OutfitsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(rpc.OutfitsResponse).Refused; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("outfits refused = %+v, want %+v", got, want)
	}

	resp, err := r.create(asOwner(), "seat")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.backend.FormState(resp.FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup("system.model"); v == nil || *v != "managed" {
		t.Fatalf("system.model = %v: the locked managed outfit must stay in force", v)
	}
}
