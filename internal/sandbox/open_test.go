package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/sandbox/local"
)

func fakeBwrapOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// A confining provider binds its workspace read-write on the host. Falling
// back to the daemon's working directory (/ under systemd) would hand an
// aria with a stale system.cwd the whole filesystem, so the open must fail.
func TestAConfiningProviderRefusesAnUnusableWorkspace(t *testing.T) {
	fakeBwrapOnPath(t)
	host := local.NewHost(local.DefaultTTL)
	file := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, ws := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "gone"),
		"not a dir": file,
		"unset":     "",
	} {
		t.Run(name, func(t *testing.T) {
			box, err := Open(context.Background(), host, nil, "bwrap", "a1", ws, t.TempDir())
			if err == nil {
				t.Fatalf("opened with workspace %q", box.Workspace)
			}
			if !strings.Contains(err.Error(), "system.cwd") {
				t.Fatalf("the error should say what to fix: %v", err)
			}
		})
	}
}

func TestAConfiningProviderOpensOnAUsableWorkspace(t *testing.T) {
	fakeBwrapOnPath(t)
	ws := t.TempDir()
	box, err := Open(context.Background(), local.NewHost(local.DefaultTTL), nil, "bwrap", "a1", ws, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(ws)
	if box.Workspace != ws && box.Workspace != want {
		t.Fatalf("workspace %q, want %q", box.Workspace, ws)
	}
}

// local confines nothing, so the fallback costs nothing and keeps a stale
// aria usable.
func TestLocalStillFallsBackToTheDaemonsDirectory(t *testing.T) {
	daemon, _ := os.Getwd()
	for _, kind := range []string{"", "local"} {
		box, err := Open(context.Background(), local.NewHost(local.DefaultTTL), nil, kind, "a1", filepath.Join(t.TempDir(), "gone"), t.TempDir())
		if err != nil {
			t.Fatalf("%q: %v", kind, err)
		}
		if box.Workspace != daemon {
			t.Fatalf("%q: workspace %q, want %q", kind, box.Workspace, daemon)
		}
	}
}
