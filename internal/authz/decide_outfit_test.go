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
		{"", `{"outfit":"narrow-decide"}`, []Resource{Outfit("narrow-decide")}},
		{"", `{"outfit":"narrow-decide","context":"long"}`, []Resource{Outfit("narrow-decide")}},
		{"", `{"outfit":"narrow-decide","provider":"x"}`, []Resource{Daemon}},
		{"", `{"outfit":"narrow-decide","model":"x"}`, []Resource{Daemon}},
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
[roles.narrow]
permissions = ["decide:outfit:narrow-decide"]

[[bind]]
role = "narrow"
principals = ["user:svc"]
`)
	s := subject(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "svc"})
	for r, want := range map[Resource]bool{
		Outfit("narrow-decide"): true,
		Outfit("narrow-group"):  false,
		Outfit("default"):       false,
		Daemon:                  false,
		Aria("narrow"):          false,
	} {
		if got := p.Allow(s, rpc.VerbDecide, r).Allowed; got != want {
			t.Errorf("decide on %v: %v, want %v", r, got, want)
		}
	}
	if p.Allow(s, rpc.VerbSend, Outfit("narrow-decide")).Allowed {
		t.Error("decide:outfit:narrow-decide granted send")
	}
}

func TestAnOutfitSelectorNamesOneOutfit(t *testing.T) {
	for sel, ok := range map[string]bool{"outfit:narrow-decide": true, "outfit:": false, "outfit:her*": false} {
		if err := (selectors{}).Validate(sel); (err == nil) != ok {
			t.Errorf("%s: %v", sel, err)
		}
	}
}
