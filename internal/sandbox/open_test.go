package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

// A form left on the removed bwrap provider must not fall back to local,
// which confines nothing; it is refused with the way out.
func TestBwrapIsRefusedAsRemoved(t *testing.T) {
	_, err := Open(context.Background(), local.NewHost(local.DefaultTTL), nil, "bwrap", "a1", t.TempDir())
	if err == nil {
		t.Fatal("opened a bwrap sandbox")
	}
	for _, want := range []string{"removed", "local", "box"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error should say %q: %v", want, err)
		}
	}
}

func TestAnUnknownSandboxIsRefused(t *testing.T) {
	_, err := Open(context.Background(), local.NewHost(local.DefaultTTL), nil, "nowhere", "a1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `unknown provider "nowhere"`) {
		t.Fatalf("err %v", err)
	}
}

// local confines nothing, so the fallback costs nothing and keeps a stale
// aria usable.
func TestLocalFallsBackToTheDaemonsDirectory(t *testing.T) {
	daemon, _ := os.Getwd()
	for _, kind := range []string{"", "local"} {
		box, err := Open(context.Background(), local.NewHost(local.DefaultTTL), nil, kind, "a1", filepath.Join(t.TempDir(), "gone"))
		if err != nil {
			t.Fatalf("%q: %v", kind, err)
		}
		if box.Workspace != daemon {
			t.Fatalf("%q: workspace %q, want %q", kind, box.Workspace, daemon)
		}
	}
}

// In house and estate mode a tool run as the daemon's uid owns the store, so
// a figaro on local, or naming no sandbox, is refused at open; the daemon
// itself is not a box.
func TestLocalIsRefusedWhenTheDaemonRunsNoToolsAsItself(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox]\nlocal_tools = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"", "local"} {
		_, err := Open(context.Background(), local.NewHost(local.DefaultTTL), loaded, kind, "a1", t.TempDir())
		if !errors.Is(err, ErrLocalToolsOff) {
			t.Fatalf("%q opened under local_tools = false: %v", kind, err)
		}
	}
}

// Where local is refused, the way out of bwrap is a box, not local.
func TestBwrapPointsOnlyAtABoxWhereLocalIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[sandbox]\nlocal_tools = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), local.NewHost(local.DefaultTTL), loaded, "bwrap", "a1", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "box") || strings.Contains(err.Error(), "to local") {
		t.Fatalf("bwrap under local_tools = false: %v", err)
	}
}
