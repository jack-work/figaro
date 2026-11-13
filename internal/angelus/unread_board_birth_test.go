package angelus_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
)

// El Inspector XVI, F1: a birth checks the caller's patch against the board
// it inherits. Read as empty, a patch that unsets system.sandbox changes
// nothing it can see, and the child is born from the real board without
// it. A board that cannot be read refuses the birth.
func TestABirthIsRefusedWhileTheBoardItInheritsCannotBeRead(t *testing.T) {
	fault := angelus.NewFormFault(nil)
	d := boxEndpointRigWith(t, defaultPolicy, "true", angelus.ReadsFormsThrough(fault))
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	var held rpc.FormCreateResponse
	if err := c.Call(context.Background(), rpc.MethodFormCreate, rpc.FormCreateRequest{Patch: namePatch("held")}, &held); err != nil {
		t.Fatal(err)
	}
	unbox := form.Build(form.Snapshot{}, nil, []string{"system.sandbox"})
	births := map[string]struct {
		parent string
		call   func() error
	}{
		"fork": {inBox.FigaroID, func() error {
			return c.Call(context.Background(), rpc.MethodFork, rpc.ForkRequest{FigaroID: inBox.FigaroID, Patch: &unbox}, nil)
		}},
		"form.bind": {held.FormID, func() error {
			return c.Call(context.Background(), rpc.MethodFormBind, rpc.FormBindRequest{Parent: held.FormID, Patch: &unbox}, nil)
		}},
	}
	for name, b := range births {
		before := d.backend.ConversationIDs()
		fault.Break(b.parent)
		err := b.call()
		fault.Heal()
		if err == nil || !strings.Contains(err.Error(), "cannot be read") {
			t.Errorf("%s unsetting system.sandbox while the parent's board cannot be read = %v, want refused", name, err)
		}
		for _, id := range d.backend.ConversationIDs() {
			if !slices.Contains(before, id) {
				t.Errorf("%s: a refused birth bore %s in sandbox %q", name, id, d.sandboxOf(t, id))
			}
		}
		if err := b.call(); err == nil {
			t.Errorf("control: %s unset system.sandbox from the box with the board readable", name)
		}
	}
}
