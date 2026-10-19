package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// A node door may be configured before any peer is: the door still needs a
// membership to ask, an empty one, and must not find none.
func TestANodeWithNoPeersStillHasAnEmptyMembership(t *testing.T) {
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: store.NewTestBackend(t)})
	if err := federate(context.Background(), a, &config.Loaded{}); err != nil {
		t.Fatal(err)
	}
	if a.Peers == nil {
		t.Fatal("no membership: the node door would panic on its first log line")
	}
	if n := a.Peers.Nodes(); len(n) != 0 {
		t.Fatalf("peers %v", n)
	}
}

// A node told to use troupe keys it does not hold yet still starts: local
// callers are served, and the keeper carries the command that fixes it.
func TestANodeWithoutItsTroupeKeysStillStarts(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[troupe]\nname = \"estate\"\n[peers.b1]\nendpoint = \"tls://127.0.0.1:7444\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: store.NewTestBackend(t)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := federate(ctx, a, loaded); err != nil {
		t.Fatalf("a missing intermediate stopped the daemon: %v", err)
	}
	if a.Troupe == nil {
		t.Fatal("no keeper")
	}
	if err := a.Troupe.Status().Err; err == nil || !strings.Contains(err.Error(), "figaro troupe intermediate estate") {
		t.Fatalf("status error %v", err)
	}
	if _, err := a.Troupe.Current(); err == nil {
		t.Fatal("a certificate to serve without keys")
	}
}
