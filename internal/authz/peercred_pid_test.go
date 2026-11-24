package authz

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/toolcgroup"
)

const toolCgroup = "/daemon/tools-ab12"

func attributing(t *testing.T, cgroup func(int) (string, error)) *Peercred {
	t.Helper()
	p, err := NewPeercred(1000, config.PeercredConfig{})
	if err != nil {
		t.Fatal(err)
	}
	reg := toolcgroup.NewRegistry()
	if _, err := reg.Register(toolCgroup, "ab12"); err != nil {
		t.Fatal(err)
	}
	p.AttributeTools(reg)
	p.cgroup = cgroup
	return p
}

func TestAToolPeerIsAttributedToItsFigaro(t *testing.T) {
	p := attributing(t, func(pid int) (string, error) { return toolCgroup, nil })
	ctx, err := p.admit(context.Background(), 1000, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := ToolOf(ctx); !ok || f != "ab12" {
		t.Fatalf("ToolOf = %q, %v", f, ok)
	}
}

func TestAPeerThatExitedIsRefused(t *testing.T) {
	p := attributing(t, func(pid int) (string, error) { return "", fmt.Errorf("toolcgroup: %w", fs.ErrNotExist) })
	if _, err := p.admit(context.Background(), 1000, 4242); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPeerWhoseCgroupIsUnreadableIsRefused(t *testing.T) {
	p := attributing(t, func(pid int) (string, error) { return "", fmt.Errorf("toolcgroup: %w", fs.ErrPermission) })
	if _, err := p.admit(context.Background(), 1000, 4242); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPeerOfAnotherUIDIsRefused(t *testing.T) {
	p := attributing(t, func(pid int) (string, error) { return "/elsewhere", nil })
	if _, err := p.admit(context.Background(), 1001, 4242); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnOrdinaryPeerCarriesNoToolMark(t *testing.T) {
	p := attributing(t, func(pid int) (string, error) { return "/user.slice/session-1", nil })
	ctx, err := p.admit(context.Background(), 1000, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ToolOf(ctx); ok {
		t.Fatal("an ordinary peer was marked as a tool")
	}
	if got := PrincipalOf(ctx); got != (rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}) {
		t.Fatalf("principal = %+v", got)
	}
}

func TestAPeerOutsideTheDaemonsPidNamespaceIsAdmittedUnattributed(t *testing.T) {
	read := func(pid int) (string, error) {
		t.Fatalf("read the cgroup of pid %d for a peer the kernel could not name", pid)
		return "", nil
	}
	p := attributing(t, read)
	ctx, err := p.admit(context.Background(), 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ToolOf(ctx); ok {
		t.Fatal("an outside peer was marked as a tool")
	}
	if got := PrincipalOf(ctx); got != (rpc.Principal{Kind: rpc.PrincipalUser, ID: "1000"}) {
		t.Fatalf("principal = %+v", got)
	}
}

func TestAnOutsidePeerOfAnotherUIDIsStillRefused(t *testing.T) {
	p := attributing(t, func(int) (string, error) { return "", nil })
	if _, err := p.admit(context.Background(), 1001, 0); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("err = %v", err)
	}
}

func TestAPeerOnAPlatformThatNamesNoPidIsNotTakenForAnOutsider(t *testing.T) {
	asked := false
	p := attributing(t, func(int) (string, error) {
		asked = true
		return "", fmt.Errorf("toolcgroup: %w", fs.ErrNotExist)
	})
	if _, err := p.admit(context.Background(), 1000, -1); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("err = %v", err)
	}
	if !asked {
		t.Fatal("an unknown pid skipped the tool check")
	}
}

// #72: under the parented layout any caller inside the daemon's unit that is
// not the daemon and belongs to no figaro is never the owner; a registered
// leftover leaf of an earlier daemon stays attributed to its aria.
func TestACallerInTheDaemonsUnitIsNeverTheOwner(t *testing.T) {
	for _, c := range []struct {
		cg   string
		pid  int32
		want string // "refused", "owner", or a figaro
	}{
		{"/u/tools/zzzz", 4242, "refused"},
		{"/u/tools", 4242, "refused"},
		{"/u/daemon", 4242, "refused"},
		{"/u", 4242, "refused"},
		{"/u/tools-gh78", 4242, "refused"}, // an unregistered leftover leaf
		{"/u/elsewhere", 4242, "refused"},
		{"/u/daemon", 42, "owner"},
		{"/user.slice/session-1", 4242, "owner"},
		{"/u/tools/cd34", 4242, "cd34"},
		{"/u/tools-ef56", 4242, "ef56"}, // a registered leftover leaf: its aria, never the owner
	} {
		p := attributing(t, func(int) (string, error) { return c.cg, nil })
		for cg, f := range map[string]string{"/u/tools/cd34": "cd34", "/u/tools-ef56": "ef56"} {
			if _, err := p.tools.Register(cg, f); err != nil {
				t.Fatal(err)
			}
		}
		p.tools.Guard("/u", 42)
		ctx, err := p.admit(context.Background(), 1000, c.pid)
		switch c.want {
		case "refused":
			if !errors.Is(err, ErrUnadmitted) {
				t.Errorf("%s pid %d: err %v, want ErrUnadmitted", c.cg, c.pid, err)
			}
		case "owner":
			if err != nil {
				t.Errorf("%s pid %d: %v", c.cg, c.pid, err)
			} else if f, ok := ToolOf(ctx); ok {
				t.Errorf("%s pid %d: marked as the tool of %s", c.cg, c.pid, f)
			}
		default:
			if err != nil {
				t.Errorf("%s pid %d: %v", c.cg, c.pid, err)
			} else if f, ok := ToolOf(ctx); !ok || f != c.want {
				t.Errorf("%s pid %d: ToolOf %q %v, want %s", c.cg, c.pid, f, ok, c.want)
			}
		}
	}
}
