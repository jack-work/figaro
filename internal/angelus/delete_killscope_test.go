package angelus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
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

// A figaro whose board cannot be read has a box nobody can name, so a delete
// that takes it refuses before it stops or takes anything; it never skips
// the box's killscope and takes the figaro anyway.
func TestADeleteIsRefusedWhileTheBoxOfAFigaroItTakesCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name   string
		broken func(root, child string) string
		req    func(root string) rpc.KillRequest
	}{
		{"kill -r --now, the root unreadable", func(root, _ string) string { return root }, func(root string) rpc.KillRequest {
			return rpc.KillRequest{FigaroID: root, Recursive: true, Now: true}
		}},
		{"kill -r --now, the child unreadable", func(_, child string) string { return child }, func(root string) rpc.KillRequest {
			return rpc.KillRequest{FigaroID: root, Recursive: true, Now: true}
		}},
		{"kill -r with a grace, the child unreadable", func(_, child string) string { return child }, func(root string) rpc.KillRequest {
			return rpc.KillRequest{FigaroID: root, Recursive: true, GraceMS: time.Hour.Milliseconds()}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := serveScopeKiller(t)
			fault := angelus.NewFormFault(nil)
			r := newRigProviding(t, defaultPolicy, fmt.Sprintf("[sandbox.boxes.judge]\nexec = %q\n", k.sock),
				func(string, provider.Knobs) (provider.Provider, error) { return &mockProviderForIntegration{}, nil },
				angelus.ReadsFormsThrough(fault))
			root, err := r.create(asOwner(), "judged")
			if err != nil {
				t.Fatal(err)
			}
			child := ariasFork(t, r, root.FigaroID)
			fault.Break(c.broken(root.FigaroID, child))
			_, err = killAs(r, c.req(root.FigaroID))
			fault.Heal()
			if err == nil || !strings.Contains(err.Error(), "cannot be read") {
				t.Fatalf("delete while a taken figaro's box cannot be read = %v, want refused", err)
			}
			for _, id := range []string{root.FigaroID, child} {
				if absent(r, id) || reapedIn(t, r, id) {
					t.Errorf("a refused delete took %s", id)
				}
			}
			if got := k.scopes(); len(got) != 0 {
				t.Errorf("a refused delete killed scopes %v", got)
			}
			if _, err := killAs(r, rpc.KillRequest{FigaroID: root.FigaroID, Recursive: true, Now: true}); err != nil {
				t.Fatalf("delete once the boards read again: %v", err)
			}
			if got := k.scopes(); !slices.Contains(got, root.FigaroID) || !slices.Contains(got, child) {
				t.Errorf("control: the healed delete killscoped %v", got)
			}
		})
	}
}
