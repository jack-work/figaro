package figaro_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
)

func TestALogRedeliversNoCut(t *testing.T) {
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	be, id := store.NewTestAria(t, "d", birth)
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{"cut":{"turn":1}}`), 0o600))
	cb, _ := form.Open("")
	cb.Apply(birth)
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Backend: be, Form: cb, Spool: spool})
	defer a.Kill()
	a.Redeliver()
	time.Sleep(500 * time.Millisecond)
	for _, m := range a.Context() {
		for _, c := range m.Content {
			t.Logf("entry role=%s sender=%q text=%q", m.Role, c.Sender, c.Text)
		}
	}
	require.Empty(t, entriesOf(a), "a log has no turn to resume")
}

func TestALogAppendPutsNoCutOnDisk(t *testing.T) {
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	be, id := store.NewTestAria(t, "d", birth)
	dir := filepath.Join(t.TempDir(), "spool")
	spool := figaro.NewSpool(dir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	cb, _ := form.Open("")
	cb.Apply(birth)
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Backend: be, Form: cb, Spool: spool})
	defer a.Kill()
	a.Redeliver()
	stop := make(chan struct{})
	seen := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(filepath.Join(dir, id+".json")); err == nil && len(b) > 0 && string(b) != "" {
				if json.Valid(b) && contains(string(b), `"cut"`) {
					select {
					case seen <- string(b):
					default:
					}
				}
			}
		}
	}()
	for i := 0; i < 200; i++ {
		a.SubmitPrompt(rpcQua("x"))
	}
	waitEntries(t, a, 200)
	close(stop)
	select {
	case s := <-seen:
		t.Fatalf("a log append put a cut on disk: %s", s)
	default:
	}
}
