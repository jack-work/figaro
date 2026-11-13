package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// The daemon opens a box's endpoint only when the box names both its outfits and
// its providers; an unlisted box's endpoint stays shut and the others still open.
func TestOnlyAListedBoxOpensItsEndpoint(t *testing.T) {
	dir := t.TempDir()
	shut := filepath.Join(dir, "shut.sock")
	open := filepath.Join(dir, "open.sock")
	anyEndpoint := filepath.Join(dir, "any.sock")
	body := fmt.Sprintf("[sandbox.boxes.bare]\nexec = %q\ndoor = %q\n[sandbox.boxes.listed]\nexec = %q\ndoor = %q\noutfits = [\"judged\"]\nproviders = [\"mock\"]\n[sandbox.boxes.vanilla]\nexec = %q\ndoor = %q\noutfits = [\"*\"]\nproviders = [\"*\"]\n",
		filepath.Join(dir, "bare-exec.sock"), shut, filepath.Join(dir, "listed-exec.sock"), open, filepath.Join(dir, "any-exec.sock"), anyEndpoint)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := angelus.New(angelus.Config{RuntimeDir: t.TempDir(), Backend: store.NewTestBackend(t)})
	a.SocketPath = filepath.Join(dir, "angelus.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.OpenBoxEndpoints(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(open); err != nil {
		t.Fatalf("canary: the listed box's door did not open, so the shut one proves nothing: %v", err)
	}
	if _, err := os.Stat(anyEndpoint); err != nil {
		t.Fatalf("a box whose lists say \"*\" did not open its door: %v", err)
	}
	if _, err := os.Stat(shut); err == nil {
		t.Fatal("a box with no outfits and no providers list opened its door")
	}
}
