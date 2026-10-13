package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

func TestBirthCwdFallsBackWhenUnusable(t *testing.T) {
	daemon, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	if got := birthCwd(dir); got != dir {
		t.Errorf("birthCwd(%q) = %q, want the requested directory", dir, got)
	}
	if got := birthCwd(filepath.Join(dir, "nowhere")); got != daemon {
		t.Errorf("an unusable request must fall back to the daemon's cwd, got %q", got)
	}
	if got := birthCwd(""); got != daemon {
		t.Errorf("an empty request must fall back to the daemon's cwd, got %q", got)
	}
}

func TestCwdFromFormFallsBackWhenUnusable(t *testing.T) {
	dir := t.TempDir()
	box, err := sandbox.Open(context.Background(), local.NewHost(local.DefaultTTL), "", "t", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := form.Open("")
	if err != nil {
		t.Fatal(err)
	}
	set := func(v string) {
		raw, _ := json.Marshal(v)
		st.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.cwd": raw}, nil))
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	set(sub)
	if got := cwdFromForm(st, box)(); got != sub {
		t.Errorf("cwdFromForm = %q, want %q", got, sub)
	}
	set(filepath.Join(dir, "nowhere"))
	if got := cwdFromForm(st, box)(); got != dir {
		t.Errorf("a stale system.cwd must fall back to the workspace, got %q", got)
	}
}
