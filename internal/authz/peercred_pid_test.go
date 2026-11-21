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
