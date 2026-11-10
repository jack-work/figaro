package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
