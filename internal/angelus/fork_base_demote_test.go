package angelus

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/store"
)

// demote picks the earliest-branched child. A child whose fork base cannot be
// read used to read as base 0 and so was always picked.
func TestDemoteDoesNotPickAChildWhoseForkBaseIsUnreadable(t *testing.T) {
	root := t.TempDir()
	b, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	if err := wire.Install(b.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	outfit, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	hs := NewHandlers(ServerConfig{Angelus: &Angelus{Registry: NewRegistry(), Backend: b}, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	top, _ := b.CreateConversation(outfit)
	_, bad, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	_, good, err := b.Fork(top)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || filepath.Base(filepath.Dir(p)) != "ir" {
			return nil
		}
		marker, _ := os.ReadFile(filepath.Join(p, ".node"))
		if slices.Contains(strings.Split(string(marker), "\n"), "trunk="+bad) {
			path = filepath.Join(p, ".fork")
			return filepath.SkipAll
		}
		return filepath.SkipDir
	})
	if path == "" {
		t.Fatal("no ir directory for the child")
	}
	if err := os.WriteFile(path, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := callDemote(t, hs, rpc.DemoteRequest{FigaroID: top}); got.Under != good {
		t.Fatalf("demote went under %q, want the readable child %s", got.Under, good)
	}
}
