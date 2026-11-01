package angelus_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const judgeAllowsMock = "providers = [\"mock\"]\n"

// A box names the providers its arias may run on; anything else is refused
// where the provider is resolved, however the aria is dressed and whoever
// mints it.
func TestABoxAriaRunsOnlyOnTheProvidersItsBoxNames(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgeAllowsMock)
	if _, err := d.create(asOwner(), "judged"); err != nil {
		t.Fatalf("canary: the listed provider was refused, so the refusal below proves nothing: %v", err)
	}
	for who, ctx := range map[string]func() error{
		"owner": func() error { _, err := d.create(asOwner(), "judged-elsewhere"); return err },
		"box": func() error {
			return d.dial(t).Call(t.Context(), rpc.MethodCreate, rpc.CreateRequest{Outfits: []string{"judged-elsewhere"}}, nil)
		},
	} {
		if err := ctx(); err == nil || !strings.Contains(err.Error(), "does not allow provider") {
			t.Errorf("%s minted a box aria on an unlisted provider: %v", who, err)
		}
	}
}

func TestABoxWithNoProviderListKeepsEveryProvider(t *testing.T) {
	d := boxDoorRigConfigured(t, defaultPolicy, "true", "")
	if _, err := d.create(asOwner(), "judged-elsewhere"); err != nil {
		t.Fatal(err)
	}
}

func TestABoxCannotDecideThroughAnUnlistedProvider(t *testing.T) {
	legit := newCountingHost(t)
	d := boxDoorRigConfigured(t, defaultPolicy, "true", judgeAllowsMock, decideThrough(legit))
	boxed, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	ask := func(prov string) error {
		return boxOnAria(t, c, rpc.MethodDecide, boxed.FigaroID, rpc.DecideRequest{FigaroID: boxed.FigaroID, Question: "go?", Provider: prov, Model: "decision-1"}, nil)
	}
	_ = ask("mock")
	if legit.hits.Load() == 0 {
		t.Fatal("canary: a listed provider's decision went nowhere, so the refusal below proves nothing")
	}
	before := legit.hits.Load()
	if err := ask("anthropic"); err == nil || !strings.Contains(err.Error(), "does not allow provider") {
		t.Errorf("a decision through an unlisted provider: %v", err)
	}
	if legit.hits.Load() != before {
		t.Fatal("the refused decision reached a provider")
	}
}
