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

func TestTheTreeAndTheListTellManagedFromUser(t *testing.T) {
	r, managed, user := layeredRig(t)
	writeFile(t, filepath.Join(managed, "role.toml"), "layers = [\"voice\"]\n[system]\nprovider = \"mock\"\n")
	writeFile(t, filepath.Join(managed, "voice.toml"), "[system]\nmodel = \"managed\"\n")
	writeFile(t, filepath.Join(user, "voice.toml"), "[system]\nmodel = \"user\"\n")

	out, err := r.call(asOwner(), rpc.MethodOutfits, rpc.OutfitsRequest{Spec: "role"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(rpc.OutfitsResponse)
	role := resp.Closure.Layers[0]
	if role.Origin != "managed" || role.Path != filepath.Join(managed, "role.toml") {
		t.Fatalf("role = %+v", role)
	}
	voice := role.Layers[0]
	wantShadow := rpc.OutfitSource{Path: filepath.Join(managed, "voice.toml"), Origin: "managed"}
	if voice.Origin != "user" || voice.Path != filepath.Join(user, "voice.toml") || len(voice.Shadowed) != 1 || voice.Shadowed[0] != wantShadow {
		t.Fatalf("voice = %+v", voice)
	}

	listed := map[string]rpc.OutfitListing{}
	for _, l := range resp.Outfits {
		listed[l.Name] = l
	}
	if l := listed["voice"]; l.Origin != "user" || len(l.Shadowed) != 1 || l.Shadowed[0] != wantShadow {
		t.Fatalf("listed voice = %+v", l)
	}
	if l := listed["role"]; l.Origin != "managed" || l.Path != filepath.Join(managed, "role.toml") {
		t.Fatalf("listed role = %+v", l)
	}
}

func TestOneRootTagsNothing(t *testing.T) {
	r := newRigConfigured(t, grantPolicy, "")
	out, err := r.call(asOwner(), rpc.MethodOutfits, rpc.OutfitsRequest{Spec: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(rpc.OutfitsResponse)
	if o := resp.Closure.Layers[0].Origin; o != "" {
		t.Fatalf("origin %q with no roots configured: the tree must read as it did", o)
	}
	for _, l := range resp.Outfits {
		if l.Origin != "" {
			t.Fatalf("listing %+v tagged with no roots configured", l)
		}
	}
}

func TestAnOutfitWriteLandsInTheOverlayOnly(t *testing.T) {
	r, managed, user := layeredRig(t)
	writeFile(t, filepath.Join(managed, "taken.toml"), "[system]\nmodel = \"managed\"\n")
	writeFile(t, filepath.Join(managed, ".locked"), "reserved\n")
	body := "[system]\nprovider = \"mock\"\n"

	out, err := r.call(asOwner(), rpc.MethodConfigure, rpc.ConfigureRequest{Outfit: "fresh", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.(rpc.ConfigureResponse).OutfitPath, filepath.Join(user, "fresh.toml"); got != want {
		t.Fatalf("wrote %s, want %s", got, want)
	}

	for _, name := range []string{"taken", "reserved"} {
		if _, err := r.call(asOwner(), rpc.MethodConfigure, rpc.ConfigureRequest{Outfit: name, Body: body}); err == nil {
			t.Errorf("%s: a write over a managed or locked name was accepted", name)
		}
		if _, err := os.Stat(filepath.Join(user, name+".toml")); err == nil {
			t.Errorf("%s: an overlay file was written for a managed or locked name", name)
		}
	}
	entries, err := os.ReadDir(managed)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("managed root holds %d entries: a write reached it", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(managed, "taken.toml")); string(b) != "[system]\nmodel = \"managed\"\n" {
		t.Fatalf("managed taken.toml changed: %q", b)
	}
}

func TestAnOverlayOfALockedOutfitsLayerIsIgnoredAndReported(t *testing.T) {
	r, managed, user := layeredRig(t)
	writeFile(t, filepath.Join(managed, "seat.toml"), "layers = [\"helper\"]\n[system]\nprovider = \"mock\"\n")
	writeFile(t, filepath.Join(managed, "helper.toml"), "[system]\nmodel = \"managed-helper\"\n")
	writeFile(t, filepath.Join(managed, ".locked"), "seat\n")
	overlay := filepath.Join(user, "helper.toml")
	writeFile(t, overlay, "[system]\nmodel = \"user-helper\"\n[system.grants]\noutfits = [\"*\"]\n")

	if _, err := r.call(asOwner(), rpc.MethodConfigure, rpc.ConfigureRequest{Refresh: true}); err != nil {
		t.Fatal(err)
	}
	out, err := r.call(asOwner(), rpc.MethodOutfits, rpc.OutfitsRequest{Spec: "seat", Fold: true})
	if err != nil {
		t.Fatal(err)
	}
	resp := out.(rpc.OutfitsResponse)
	want := rpc.OutfitLockedOverlay{Locked: "seat", Layer: "helper", Path: overlay}
	if got := resp.LockedLayerOverlays; len(got) != 1 || got[0] != want {
		t.Fatalf("locked layer overlays = %+v, want %+v", got, want)
	}
	helper := resp.Closure.Layers[0].Layers[0]
	if helper.Path != filepath.Join(managed, "helper.toml") || len(helper.Overlaid) != 1 || helper.Overlaid[0].Path != overlay {
		t.Fatalf("helper = %+v", helper)
	}
	if v := resp.Form.Lookup("system.model"); v == nil || *v != "managed-helper" {
		t.Fatalf("system.model = %v: the managed helper must stay in force", v)
	}
	if v := resp.Form.Lookup("system.grants.outfits"); v != nil {
		t.Fatalf("system.grants.outfits = %v: an overlay of a locked outfit's layer granted outfits", *v)
	}

	resp2, err := r.create(asOwner(), "seat")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.backend.FormState(resp2.FigaroID)
	if err != nil {
		t.Fatal(err)
	}
	if v := snap.Lookup("system.model"); v == nil || *v != "managed-helper" {
		t.Fatalf("born system.model = %v", v)
	}
}
