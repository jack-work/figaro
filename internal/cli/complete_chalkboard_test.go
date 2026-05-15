package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
)

func TestCompleteChalkboardKeys_IncludesAllKnownAndExpandsEnv(t *testing.T) {
	got := completeChalkboardKeys(nil)
	gotSet := map[string]struct{}{}
	for _, k := range got {
		gotSet[k] = struct{}{}
	}

	// Every non-templated well-known key must appear verbatim.
	for _, d := range chalkboard.WellKnownKeys() {
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
	for _, name := range chalkboard.EnvironmentAllowlist {
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

func TestSoftFetchLiveKeysReturnsNilWhenDaemonDown(t *testing.T) {
	// No daemon is started in the test environment; the call must
	// fail soft and return nil within the timeout.
	if got := softFetchLiveKeys(); got != nil {
		t.Errorf("expected nil when daemon unavailable, got %v", got)
	}
}
