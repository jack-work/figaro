package command

import "testing"

func TestEveryWordResolvesToOneCommandPerSurface(t *testing.T) {
	shell, pager := map[string]string{}, map[string]string{}
	claim := func(surface map[string]string, w, name string) {
		if prev, ok := surface[w]; ok {
			t.Errorf("%q names both %s and %s", w, prev, name)
		}
		surface[w] = name
	}
	for _, c := range All() {
		if c.PagerOnly {
			for _, w := range c.Names() {
				claim(pager, w, c.Name)
			}
		}
	}
	pagerOnly := map[string]bool{}
	for w := range pager {
		pagerOnly[w] = true
	}
	for _, c := range All() {
		if c.PagerOnly {
			continue
		}
		for _, w := range c.Names() {
			claim(shell, w, c.Name)
		}
		for _, w := range append(c.Names(), c.PagerAliases...) {
			if !pagerOnly[w] {
				claim(pager, w, c.Name)
			}
		}
	}
	for w, name := range shell {
		if got, _ := Shell(w); got.Name != name {
			t.Errorf("Shell(%q) = %q, want %q", w, got.Name, name)
		}
	}
	for w, name := range pager {
		if got, _ := Pager(w); got.Name != name {
			t.Errorf("Pager(%q) = %q, want %q", w, got.Name, name)
		}
	}
}

func TestThePagerOnlyWordsWinInThePager(t *testing.T) {
	if c, _ := Shell("history"); c.Name != "show" {
		t.Fatalf("shell history = %q, want show", c.Name)
	}
	if c, _ := Pager("history"); c.Name != "history" || !c.PagerOnly {
		t.Fatalf("pager history = %+v, want the pager's own", c)
	}
	if _, ok := Shell("notifications"); ok {
		t.Fatal("notifications is a pager verb, not a shell one")
	}
	if c, _ := Pager("s"); c.Name != "send" {
		t.Fatalf("pager s = %q, want send", c.Name)
	}
}

func TestEveryEntryIsDescribed(t *testing.T) {
	kinds, modes, sources := map[Kind]bool{}, map[Mode]bool{}, map[Source]bool{}
	for _, k := range Kinds() {
		kinds[k] = true
	}
	for _, m := range Modes() {
		modes[m] = true
	}
	for _, s := range Sources() {
		sources[s] = true
	}
	for _, c := range All() {
		if c.Help == "" && !c.Hidden {
			t.Errorf("%s has no help", c.Name)
		}
		if c.Complete != "" && !sources[c.Complete] {
			t.Errorf("%s completes from %q, which Sources does not list", c.Name, c.Complete)
		}
		if !modes[c.Pager] {
			t.Errorf("%s: pager mode %q", c.Name, c.Pager)
		}
		if (c.Pager == Refused) != (c.Refusal != "") {
			t.Errorf("%s: a refusal is given exactly when the pager refuses (%q, %q)", c.Name, c.Pager, c.Refusal)
		}
		for i, a := range c.Args {
			if !kinds[a.Kind] {
				t.Errorf("%s arg %s: kind %q", c.Name, a.Name, a.Kind)
			}
			if a.Repeats && i != len(c.Args)-1 {
				t.Errorf("%s arg %s repeats but is not last", c.Name, a.Name)
			}
			for _, ch := range a.Choices {
				if ch.Name == "" || ch.Help == "" {
					t.Errorf("%s arg %s: choice %+v", c.Name, a.Name, ch)
				}
			}
		}
		for _, f := range c.Flags {
			if f.Value != "" && !kinds[f.Value] {
				t.Errorf("%s --%s: value kind %q", c.Name, f.Long, f.Value)
			}
		}
		if c.Complete == Choices && len(c.Choices()) == 0 {
			t.Errorf("%s completes choices it does not have", c.Name)
		}
	}
}

func TestBoundsFollowTheArgs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		min, max int
	}{{"set", 2, 2}, {"drop", 1, 2}, {"get", 1, 0}, {"show", 0, 1}, {"troupe", 1, 4}} {
		c, _ := Shell(tc.name)
		if min, max := c.Bounds(); min != tc.min || max != tc.max {
			t.Errorf("%s bounds = %d..%d, want %d..%d", tc.name, min, max, tc.min, tc.max)
		}
	}
}
