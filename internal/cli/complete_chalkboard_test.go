package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

func TestCompleteFormKeys_IncludesAllKnownAndExpandsEnv(t *testing.T) {
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
// where their own angelus is listening — and FIGARO_ARIA, which names a
// live aria. A test whose premise is "the daemon is down" must establish
// that premise itself rather than hope for it.
func isolateDaemonEnv(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FIGARO_RUNTIME_DIR", filepath.Join(root, "run")) // no socket lives here
	t.Setenv("FIGARO_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("FIGARO_ARIA", "") // no ambient identity to resolve
}

func TestSoftFetchLiveKeysReturnsNilWhenDaemonDown(t *testing.T) {
	// The premise, established rather than assumed: no daemon is
	// reachable, so the call must fail soft and return nil within the
	// timeout. Without this the test found the developer's own angelus
	// and returned their live form keys.
	isolateDaemonEnv(t)
	if got := softFetchLiveKeys(); got != nil {
		t.Errorf("expected nil when daemon unavailable, got %v", got)
	}
}
