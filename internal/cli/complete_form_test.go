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

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
)

func TestCompleteFormKeys_IncludesAllKnownAndExpandsEnv(t *testing.T) {
	// Isolated so the candidates come from the catalog alone: with a
	// real daemon reachable the developer's own bound aria would leak
	// keys in: or, worse, a fetch failure would empty the list.
	isolateDaemonEnv(t)
	got := completeFormKeys(nil)
	gotSet := map[string]struct{}{}
	for _, k := range got {
		gotSet[k] = struct{}{}
	}

	// Every non-templated well-known key must appear verbatim.
	for _, d := range form.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			continue
		}
		if _, ok := gotSet[d.Key]; !ok {
			t.Errorf("missing well-known key %q in candidates", d.Key)
		}
	}

	// The <name> placeholder for system.environment must NOT leak
	// through as a literal candidate.
	if _, ok := gotSet["system.environment.<name>"]; ok {
		t.Error("placeholder leaked as candidate")
	}

	// Each allowlisted env var must produce an entry.
	for _, name := range form.EnvironmentAllowlist {
		want := "system.environment." + strings.ToLower(name)
		if _, ok := gotSet[want]; !ok {
			t.Errorf("missing expanded env entry %q", want)
		}
	}

	// Output must be sorted.
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("not sorted at index %d: %q > %q", i, got[i-1], got[i])
			break
		}
	}
}

// isolateDaemonEnv points every daemon-facing lookup at empty temp dirs,
// so a test that says "no daemon" actually has none.
//
// The environment is not a given: a developer running `go test ./...` from
// a normal shell inherits FIGARO_RUNTIME_DIR (or its default), which is
// where their own angelus is listening, and FIGARO_ARIA, which names a
// live aria. A test whose premise is "the daemon is down" must establish
// that premise itself rather than hope for it.
func isolateDaemonEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", filepath.Join(root, "run")) // no socket lives here
	t.Setenv("FIGARO_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("FIGARO_ARIA", "") // no ambient identity to resolve
}

func TestSoftFetchLiveKeysUnboundWhenDaemonDown(t *testing.T) {
	// The premise, established rather than assumed: no daemon is
	// reachable, so the call must fail soft, and report UNBOUND, not
	// fetch-failed, because with no daemon nothing can be bound and
	// the catalog is the honest answer. Without the isolation the test
	// found the developer's own angelus and returned their live keys.
	isolateDaemonEnv(t)
	got, status, err := softFetchLiveKeys()
	if got != nil {
		t.Errorf("expected nil keys when daemon unavailable, got %v", got)
	}
	if status != liveKeysUnbound {
		t.Errorf("expected liveKeysUnbound when daemon unavailable, got %v (err %v)", status, err)
	}
}

// fakeRPCServer serves a jkrpc handler map on a unix socket, standing in
// for the angelus or an aria endpoint. Just enough daemon to answer the
// completion path's calls.
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

// fakeAngelusResolvingTo answers pid.resolve with a binding pointing at
// ariaSock, on the socket path completion actually dials. It is STRICT
// about the pid, the way the real daemon is: a resolve for any pid but
// wantPID answers not-found. The first version of this fake said Found
// to every pid and was thereby tidier than reality: it certified a
// completion path whose shellPID was never initialized (the __complete
// dispatch exits before initBindingPolicy), which the real daemon
// answered with not-found for pid 0, and every pid-bound shell got the
// catalog instead of its aria's keys.
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

// bindShellPID computes the real binding key (initBindingPolicy is what
// the production paths run) and returns it, restoring the package state
// after the test. A test that hand-set shellPID would certify nothing
// about the init that the __complete dispatch once skipped.
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

// completionTestDirs builds a SHORT runtime dir (unix socket paths cap
// near 108 bytes; t.TempDir() embeds the full test name) and points the
// completion path at it.
func completionTestDirs(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "figcomp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("FIGARO_RUNTIME_DIR", root)
	t.Setenv("FIGARO_ARIA", "") // pid binding, not static attendance
	t.Setenv("FIGARO_NO_BIND", "")
	return root
}

// The brief's test, verbatim: an attended aria whose form carries
// skills.foo must complete skills.foo. It would have failed the day the
// live fetch broke, because skills.* keys are materialized from an
// outfit at birth and can never appear in a static catalog.
func TestCompleteFormKeys_AttendedAriaOffersLiveKeys(t *testing.T) {
	rt := completionTestDirs(t)
	ariaSock := filepath.Join(rt, "aria.sock")
	fakeAngelusResolvingTo(t, rt, ariaSock, bindShellPID(t))
	fakeRPCServer(t, ariaSock, map[string]jkrpc.HandlerFunc{
		rpc.MethodForm: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var snap form.Snapshot
			if err := json.Unmarshal([]byte(`{"skills.foo":"bar","duke-title":"Gluck"}`), &snap); err != nil {
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
	// The catalog still rides along when the fetch SUCCEEDS: settable
	// keys the board doesn't hold yet are legitimate `set` targets.
	if _, ok := gotSet["system.model"]; !ok {
		t.Errorf("well-known key system.model missing alongside live keys")
	}
}

// The other half of the distinction: an aria is bound but its endpoint
// is dead. Completion must DECLINE: offering the catalog here would
// present documentation as the board's state, which is the defect this
// distinction exists to remove.
func TestCompleteFormKeys_DeclinesWhenBoundFormUnreadable(t *testing.T) {
	rt := completionTestDirs(t)
	// The resolved endpoint has no listener: dial fails, fetch fails.
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
