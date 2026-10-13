package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/jkrpc"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
)

func TestCompleteFormKeys_IncludesAllKnownAndExpandsEnv(t *testing.T) {
	// Isolated: a reachable daemon would leak the developer's own bound aria in.
	isolateDaemonEnv(t)
	got := completeFormKeys(nil)
	gotSet := map[string]struct{}{}
	for _, k := range got {
		gotSet[k] = struct{}{}
	}

	for _, d := range form.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			continue
		}
		if _, ok := gotSet[d.Key]; !ok {
			t.Errorf("missing well-known key %q in candidates", d.Key)
		}
	}

	if _, ok := gotSet["system.environment.<name>"]; ok {
		t.Error("placeholder leaked as candidate")
	}

	for _, name := range form.EnvironmentAllowlist {
		want := "system.environment." + strings.ToLower(name)
		if _, ok := gotSet[want]; !ok {
			t.Errorf("missing expanded env entry %q", want)
		}
	}

	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("not sorted at index %d: %q > %q", i, got[i-1], got[i])
			break
		}
	}
}

// isolateDaemonEnv points every daemon-facing lookup at empty temp dirs: a
// normal shell inherits FIGARO_RUNTIME_DIR and FIGARO_ARIA.
func isolateDaemonEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("FIGARO_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("FIGARO_ARIA", "")
}

func TestSoftFetchLiveKeysUnboundWhenDaemonDown(t *testing.T) {
	// With no daemon nothing can be bound, so the status is unbound, not
	// fetch-failed.
	isolateDaemonEnv(t)
	got, status, err := softFetchLiveKeys()
	if got != nil {
		t.Errorf("expected nil keys when daemon unavailable, got %v", got)
	}
	if status != liveKeysUnbound {
		t.Errorf("expected liveKeysUnbound when daemon unavailable, got %v (err %v)", status, err)
	}
}

func fakeRPCServer(t *testing.T, sockPath string, handlers map[string]jkrpc.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen %s: %v", sockPath, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		ln.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			srv := jkrpc.NewServer(jkrpc.NewConn(conn), handlers)
			go srv.Serve(ctx)
		}
	}()
}

// fakeAngelusResolvingTo is strict about the pid, as the real daemon is: any
// pid but wantPID is not-found.
func fakeAngelusResolvingTo(t *testing.T, runtimeDir, ariaSock string, wantPID int) {
	t.Helper()
	fakeRPCServer(t, filepath.Join(runtimeDir, "angelus.sock"), map[string]jkrpc.HandlerFunc{
		rpc.MethodResolve: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var req rpc.ResolveRequest
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
			if req.PID != wantPID {
				return rpc.ResolveResponse{Found: false}, nil
			}
			return rpc.ResolveResponse{
				FigaroID: "fake-aria",
				Endpoint: rpc.Endpoint{Scheme: "unix", Address: ariaSock},
				Found:    true,
			}, nil
		},
	})
}

// bindShellPID runs the real initBindingPolicy, restoring package state after the test.
func bindShellPID(t *testing.T) int {
	t.Helper()
	prev := shellPID
	t.Cleanup(func() { shellPID = prev })
	initBindingPolicy()
	if shellPID == 0 {
		t.Fatal("initBindingPolicy left shellPID at 0")
	}
	return shellPID
}

// Unix socket paths cap near 108 bytes; t.TempDir() embeds the full test name.
func completionTestDirs(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "figcomp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("FIGARO_RUNTIME_DIR", root)
	t.Setenv("FIGARO_ARIA", "")
	t.Setenv("FIGARO_NO_BIND", "")
	return root
}

// skills.* keys are materialized from an outfit at birth and never appear in a
// static catalog.
func TestCompleteFormKeys_AttendedAriaOffersLiveKeys(t *testing.T) {
	rt := completionTestDirs(t)
	ariaSock := filepath.Join(rt, "aria.sock")
	fakeAngelusResolvingTo(t, rt, ariaSock, bindShellPID(t))
	fakeRPCServer(t, ariaSock, map[string]jkrpc.HandlerFunc{
		rpc.MethodForm: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var snap form.Snapshot
			if err := json.Unmarshal([]byte(`{"skills.foo":"bar","duke-title":"Duke"}`), &snap); err != nil {
				return nil, err
			}
			return rpc.FormResponse{Snapshot: snap, Version: 1}, nil
		},
	})

	got := completeFormKeys(nil)
	gotSet := map[string]struct{}{}
	for _, k := range got {
		gotSet[k] = struct{}{}
	}
	if _, ok := gotSet["skills.foo"]; !ok {
		t.Fatalf("attended aria's live key skills.foo missing from candidates: %v", got)
	}
	// The catalog still rides along on success: settable keys the board lacks
	// are legitimate `set` targets.
	if _, ok := gotSet["system.model"]; !ok {
		t.Errorf("well-known key system.model missing alongside live keys")
	}
}

// A bound aria with a dead endpoint must decline: the catalog would present
// documentation as the board's state.
func TestCompleteFormKeys_DeclinesWhenBoundFormUnreadable(t *testing.T) {
	rt := completionTestDirs(t)
	fakeAngelusResolvingTo(t, rt, filepath.Join(rt, "dead.sock"), bindShellPID(t))

	keys, status, err := softFetchLiveKeys()
	if status != liveKeysFetchFailed {
		t.Fatalf("expected liveKeysFetchFailed for a dead aria endpoint, got %v (keys %v, err %v)", status, keys, err)
	}
	if err == nil {
		t.Error("fetch-failed must carry the failing step; got nil error")
	}
	if got := completeFormKeys(nil); got != nil {
		t.Errorf("expected no candidates when the bound form is unreadable, got %v", got)
	}
}
