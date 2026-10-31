package angelus_test

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// Shell bindings are keyed by bare pid, and a box has its own pid namespace:
// a box that could bind would attach host shells to its arias.
func TestShellBindingsStayRefusedToABox(t *testing.T) {
	d := boxDoorRig(t, "true")
	inBox, err := d.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	c := d.dial(t)
	ctx := context.Background()
	if err := c.Call(ctx, rpc.MethodBind, rpc.BindRequest{PID: 1, FigaroID: inBox.FigaroID}, nil); err == nil {
		t.Fatal("the box bound a shell")
	}
	if err := c.Call(ctx, rpc.MethodResolve, rpc.ResolveRequest{PID: 1}, nil); err == nil {
		t.Fatal("the box resolved a shell")
	}
	if err := c.Call(ctx, rpc.MethodUnbind, rpc.UnbindRequest{PID: 1}, nil); err == nil {
		t.Fatal("the box unbound a shell")
	}
}
