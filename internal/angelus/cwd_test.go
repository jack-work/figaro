package angelus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
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
	daemon, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := form.Open("")
	if err != nil {
		t.Fatal(err)
	}
	set := func(v string) {
		raw, _ := json.Marshal(v)
		st.Apply(form.Patch{Set: map[string]json.RawMessage{"system.cwd": raw}})
	}

	set(dir)
	if got := cwdFromForm(st)(); got != dir {
		t.Errorf("cwdFromForm = %q, want %q", got, dir)
	}
	set(filepath.Join(dir, "nowhere"))
	if got := cwdFromForm(st)(); got != daemon {
		t.Errorf("a stale system.cwd must fall back to the daemon's cwd, got %q", got)
	}
}
