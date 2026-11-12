package authz

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkauthz"
)

func TestADecideIsCheckedOnWhatItNamesAndAnOverrideOnTheDaemon(t *testing.T) {
	cases := []struct {
		aria, params string
		want         []Resource
	}{
		{"", `{}`, []Resource{Outfit("default")}},
		{"", `{"outfit":"herald-decide"}`, []Resource{Outfit("herald-decide")}},
		{"", `{"outfit":"herald-decide","context":"long"}`, []Resource{Outfit("herald-decide")}},
		{"", `{"outfit":"herald-decide","provider":"x"}`, []Resource{Daemon}},
		{"", `{"outfit":"herald-decide","model":"x"}`, []Resource{Daemon}},
		{"", `{"api":"openai"}`, []Resource{Daemon}},
		{"x", `{"figaro_id":"x"}`, []Resource{Aria("x")}},
		{"x", `{"figaro_id":"x","provider":"p"}`, []Resource{Aria("x"), Daemon}},
		{"x", `{"figaro_id":"x","model":"m"}`, []Resource{Aria("x"), Daemon}},
		{"x", `{"figaro_id":"x","api":"openai"}`, []Resource{Aria("x"), Daemon}},
	}
	for _, c := range cases {
		got, err := decideResources(c.aria, json.RawMessage(c.params))
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v, want %v", c.params, got, err, c.want)
		}
	}
	if _, err := decideResources("", json.RawMessage(`{"outfit":3}`)); err == nil {
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
