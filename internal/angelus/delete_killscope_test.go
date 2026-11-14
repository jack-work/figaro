package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/jkrpc"
)

type scopeKiller struct {
	sock   string
	mu     sync.Mutex
	killed []string
}

func (s *scopeKiller) scopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.killed)
}

func serveScopeKiller(t *testing.T) *scopeKiller {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "killer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	k := &scopeKiller{sock: filepath.Join(dir, "exec.sock")}
	ln, err := net.Listen("unix", k.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			var scope string
			srv := jkrpc.NewServer(jkrpc.NewConn(nc), map[string]jkrpc.HandlerFunc{
				"box.open": func(_ context.Context, p json.RawMessage) (any, error) {
					var req struct {
						Scope string `json:"scope"`
					}
					_ = json.Unmarshal(p, &req)
					scope = req.Scope
					return map[string]any{"workspace": dir}, nil
				},
				"box.session.killscope": func(context.Context, json.RawMessage) (any, error) {
					k.mu.Lock()
					k.killed = append(k.killed, scope)
					k.mu.Unlock()
					return 0, nil
				},
			})
			go func() { srv.Serve(ctx); nc.Close() }()
		}
	}()
	return k
}

// El Inspector XVI: a delete takes the root and every descendant in its plan,
// and each one's sessions in its box go with it, not the root's alone.
func TestADeleteKillsTheBoxedSessionsOfEveryFigaroItTakes(t *testing.T) {
	k := serveScopeKiller(t)
	r := newRigConfigured(t, defaultPolicy, fmt.Sprintf("[sandbox.boxes.judge]\nexec = %q\n", k.sock))
	root, err := r.create(asOwner(), "judged")
	if err != nil {
		t.Fatal(err)
	}
	child := ariasFork(t, r, root.FigaroID)
	if box := r.sandboxOf(t, child); box != "judge" {
		t.Fatalf("control: the child runs in %q, want judge", box)
	}
	if _, err := killAs(r, rpc.KillRequest{FigaroID: root.FigaroID, Recursive: true, Now: true}); err != nil {
		t.Fatal(err)
	}
	if !absent(r, child) {
		t.Fatal("control: the recursive delete left the child")
	}
	got := k.scopes()
	for _, id := range []string{root.FigaroID, child} {
		if !slices.Contains(got, id) {
			t.Errorf("no killscope for %s; the box saw %v", id, got)
		}
	}
}
