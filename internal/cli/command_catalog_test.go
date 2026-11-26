package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/command"
)

// The router is built from api/command, so a verb registered around it, or an
// entry no verb answers, is drift the web would inherit.
func TestTheRouterIsTheCatalog(t *testing.T) {
	r := buildRouter("figaro", nil)
	registered := map[string]bool{}
	for _, c := range r.Commands() {
		if c.Name == "__complete" {
			continue
		}
		registered[c.Name] = true
		spec, ok := command.Shell(c.Name)
		if !ok || spec.Name != c.Name {
			t.Errorf("%s is registered but has no api/command entry", c.Name)
			continue
		}
		if !reflect.DeepEqual(c.Aliases, spec.Aliases) || c.Short != spec.Help || c.Usage != spec.Usage || c.Group != spec.Group || c.PassRaw != spec.Raw || c.Hidden != spec.Hidden {
			t.Errorf("%s: the router's description is not the catalog's", c.Name)
		}
		flags := c.Flags
		if c.PassRaw {
			flags = c.CompleteFlags
		}
		if !reflect.DeepEqual(flags, flagDefs(spec.Flags)) {
			t.Errorf("%s: the router's flags are not the catalog's", c.Name)
		}
		if min, max := spec.Bounds(); !c.PassRaw && (c.ArgsMin != min || c.ArgsMax != max) {
			t.Errorf("%s: args %d..%d, catalog %d..%d", c.Name, c.ArgsMin, c.ArgsMax, min, max)
		}
		if _, ok := completers[spec.Complete]; spec.Complete != "" && spec.Complete != command.Commands && !ok {
			t.Errorf("%s completes from %q, which no completer serves", c.Name, spec.Complete)
		}
	}
	for _, spec := range command.All() {
		if !spec.PagerOnly && !registered[spec.Name] {
			t.Errorf("api/command lists %s, which the router does not run", spec.Name)
		}
	}
}

// The pager's routing as it stood before the catalog: a catalog edit that moves
// a word between modes, or a handler the catalog does not name, fails here.
var pagerModes = map[string]command.Mode{
	"send": command.Hosted, "s": command.Hosted, "qua": command.Hosted,
	"new": command.Hosted, "listen": command.Hosted, "fork": command.Hosted,
	"attend": command.Hosted, "at": command.Hosted,
	"back": command.Hosted, "aria-back": command.Hosted,
	"forward": command.Hosted, "aria-forward": command.Hosted,
	"kill": command.Hosted, "succeed": command.Hosted, "decide": command.Hosted,
	"notifications": command.Hosted, "no": command.Hosted, "history": command.Hosted,
	"login": command.Hosted, "comment": command.Hosted, "sort": command.Hosted,
	"replay": command.Refused, "import": command.Refused,
	"list": command.View, "ls": command.View, "lsh": command.View, "recent": command.View,
	"reaped": command.View, "troupe": command.View, "models": command.View,
	"state": command.View, "form": command.View,
}

var keyboardSubverbs = []string{
	"provider login", "provider logout",
	"troupe login", "troupe join", "troupe leave",
	"vault init", "vault reset", "vault unlock",
}

func TestThePagerDispatchesByTheCatalog(t *testing.T) {
	for _, spec := range command.All() {
		for _, w := range append(spec.Names(), spec.PagerAliases...) {
			if c, _ := command.Pager(w); c.Name != spec.Name {
				continue
			}
			want, pinned := pagerModes[w]
			if !pinned {
				want = command.Routed
			}
			if spec.Pager != want {
				t.Errorf(":%s is %s in the catalog; the pager routed it %s", w, spec.Pager, want)
			}
			_, handled := overlayFor(w)
			hosted := spec.Pager == command.Hosted && spec.Name != "login" && spec.Name != "comment"
			if hosted != handled {
				t.Errorf(":%s: hosted=%v in the catalog, pager handler=%v", w, hosted, handled)
			}
			if spec.Pager == command.Refused && spec.Refusal == "" {
				t.Errorf(":%s is refused with no reason", w)
			}
			_, pit := formPitFor(w)
			if wantPit := spec.Pager == command.View && spec.Name != "state"; pit != wantPit {
				t.Errorf(":%s: view=%v in the catalog, pit=%v", w, spec.Pager == command.View, pit)
			}
			if _, view, _ := topologyFormPit([]string{w}); view != pit {
				t.Errorf(":%s: formPitFor=%v but topologyFormPit=%v", w, pit, view)
			}
			if switchesSubject(w) != spec.Switches {
				t.Errorf(":%s switches=%v, catalog %v", w, switchesSubject(w), spec.Switches)
			}
		}
	}
	for name := range overlays {
		if c, ok := command.Pager(name); !ok || c.Name != name || c.Pager != command.Hosted {
			t.Errorf("the pager hosts :%s, which the catalog does not list as hosted", name)
		}
	}
	var keyboard []string
	for _, spec := range command.All() {
		for _, ch := range spec.Choices() {
			if ch.Keyboard {
				keyboard = append(keyboard, spec.Name+" "+ch.Name)
			}
		}
	}
	slices.Sort(keyboard)
	want := slices.Sorted(slices.Values(keyboardSubverbs))
	if !slices.Equal(keyboard, want) {
		t.Errorf("subverbs that read the keyboard: catalog %v, pager %v", keyboard, want)
	}
	for _, sv := range keyboardSubverbs {
		if !readsKeyboard(strings.Fields(sv)) {
			t.Errorf(":%s reads the keyboard but the pager would run it", sv)
		}
	}
	for _, line := range []string{"state apply x=1", "form unset mantra", "state set mantra x"} {
		if _, _, ok := liveForm(tokenize(line)); ok {
			t.Errorf(":%s is a state verb, not a form read", line)
		}
	}
}
