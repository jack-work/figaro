package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type failingSweeper struct{ calls, removing int }

func (f *failingSweeper) SweepUnmarkedNodes(remove bool) (store.SweepReport, error) {
	f.calls++
	if remove {
		f.removing++
	}
	return store.SweepReport{}, errors.New("disk on fire")
}

// The sweep is an optional cleanup: its failure, or a bad setting, is logged
// and the daemon goes on to serve.
func TestAFailingNodeSweepNeverStopsTheDaemon(t *testing.T) {
	for body, want := range map[string]struct{ calls, removing int }{
		"":                                {1, 0},
		"[store]\nnode_sweep = \"on\"\n":  {1, 1},
		"[store]\nnode_sweep = \"off\"\n": {0, 0},
		"[store]\nnode_sweep = \"yes\"\n": {0, 0},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		f := &failingSweeper{}
		sweepNodes(loaded, f)
		if f.calls != want.calls || f.removing != want.removing {
			t.Errorf("%q: sweep calls %d (removing %d), want %d (%d)", body, f.calls, f.removing, want.calls, want.removing)
		}
	}
}
