package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/cmdkit"
)

func TestContainsShellUnsafe(t *testing.T) {
	cases := map[string]bool{
		"normal.txt":     false,
		"with space":     true,
		"with\ttab":      true,
		"quote'd":        true,
		"a$b":            true,
		"glob*":          true,
		"pipe|me":        true,
		"plain-name_123": false,
		"":               false,
	}
	for in, want := range cases {
		if got := containsShellUnsafe(in); got != want {
			t.Errorf("containsShellUnsafe(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestListCWD_FiltersHiddenAndUnsafe(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "visible.txt"), nil, 0o600))
	must(os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0o600))
	must(os.WriteFile(filepath.Join(dir, "with space.txt"), nil, 0o600))
	must(os.Mkdir(filepath.Join(dir, "subdir"), 0o700))

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(prev)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	got := listCWD()
	gotSet := map[string]struct{}{}
	for _, n := range got {
		gotSet[n] = struct{}{}
	}

	if _, ok := gotSet["visible.txt"]; !ok {
		t.Errorf("missing visible.txt in %v", got)
	}
	if _, ok := gotSet["subdir/"]; !ok {
		t.Errorf("missing subdir/ (with trailing slash) in %v", got)
	}
	if _, ok := gotSet[".hidden"]; ok {
		t.Errorf("hidden file leaked: %v", got)
	}
	if _, ok := gotSet["with space.txt"]; ok {
		t.Errorf("unsafe filename leaked: %v", got)
	}
}

func TestCompletePromptContext_UnionOfChalkboardAndCWD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	got := completePromptContext(nil)
	gotSet := map[string]struct{}{}
	for _, n := range got {
		gotSet[n] = struct{}{}
	}

	if _, ok := gotSet["marker.go"]; !ok {
		t.Errorf("missing CWD entry marker.go in %v", got)
	}

	// At least one well-known chalkboard key must be present.
	found := false
	for _, d := range chalkboard.WellKnownKeys() {
		if strings.HasSuffix(d.Key, "<name>") {
			continue
		}
		if _, ok := gotSet[d.Key]; ok {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no well-known chalkboard key in candidates: %v", got)
	}

	// Output must be sorted (callers and shells assume nothing, but
	// stable order is friendly).
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("not sorted at %d: %q > %q", i, got[i-1], got[i])
			break
		}
	}
}

func TestCompletePromptOrIDFlag_Routing(t *testing.T) {
	// nil ctx -> nil, no panic.
	if got := completePromptOrIDFlag(nil); got != nil {
		t.Errorf("expected nil for nil ctx, got %v", got)
	}

	// Plain (no separator, no --id): nil.
	got := completePromptOrIDFlag(&cmdkit.CompleteContext{Args: []string{"--verbose"}})
	if got != nil {
		t.Errorf("expected nil for unrelated flag, got %v", got)
	}

	// After --id: aria ids (daemon may or may not be up; if up the
	// list is non-empty, if down it's nil — both acceptable; what we
	// assert is that chalkboard/CWD keys are NOT present, which
	// distinguishes the --id branch from the past-separator branch).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "should_not_appear.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	defer os.Chdir(prev)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	idCands := completePromptOrIDFlag(&cmdkit.CompleteContext{Args: []string{"--id"}})
	for _, c := range idCands {
		if c == "should_not_appear.txt" {
			t.Errorf("CWD entry leaked into --id branch: %v", idCands)
		}
	}

	// PastSeparator: full pool, CWD entry must appear.
	pastCands := completePromptOrIDFlag(&cmdkit.CompleteContext{
		Args:          []string{"--"},
		PastSeparator: true,
	})
	found := false
	for _, c := range pastCands {
		if c == "should_not_appear.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("CWD entry missing from past-separator branch: %v", pastCands)
	}
}
