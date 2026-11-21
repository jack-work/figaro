package cli

import (
	"reflect"
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
