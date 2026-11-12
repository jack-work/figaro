package authz

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func TestADecideIsCheckedAgainstTheOutfitItNames(t *testing.T) {
	for params, want := range map[string]Resource{
		`{}`:                         Outfit("default"),
		`{"outfit":"herald-decide"}`: Outfit("herald-decide"),
		`{"outfit":"herald-decide","provider":"x"}`:   Daemon,
		`{"outfit":"herald-decide","model":"x"}`:      Daemon,
		`{"api":"openai"}`:                            Daemon,
		`{"outfit":"herald-decide","context":"long"}`: Outfit("herald-decide"),
	} {
		got, err := decideResource(json.RawMessage(params))
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", params, got, err, want)
		}
	}
	if _, err := decideResource(json.RawMessage(`{"outfit":3}`)); err == nil {
		t.Error("an unparseable decide was given a resource")
	}
}

func TestAnOutfitGrantMatchesOnlyThatOutfit(t *testing.T) {
	p := loaded(t, `
[roles.herald]
permissions = ["decide:outfit:herald-decide"]

[[bind]]
role = "herald"
principals = ["user:herald"]
`)
	s := subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "herald"})
	for r, want := range map[Resource]bool{
		Outfit("herald-decide"): true,
		Outfit("herald-group"):  false,
		Outfit("default"):       false,
		Daemon:                  false,
		Aria("herald"):          false,
	} {
		if got := p.Allow(s, rpc.VerbDecide, r).Allowed; got != want {
			t.Errorf("decide on %v: %v, want %v", r, got, want)
		}
	}
	if p.Allow(s, rpc.VerbSend, Outfit("herald-decide")).Allowed {
		t.Error("decide:outfit:herald-decide granted send")
	}
}

func TestAnOutfitSelectorNamesOneOutfit(t *testing.T) {
	for sel, ok := range map[string]bool{"outfit:herald-decide": true, "outfit:": false, "outfit:her*": false} {
		if err := (selectors{}).Validate(sel); (err == nil) != ok {
			t.Errorf("%s: %v", sel, err)
		}
	}
}
