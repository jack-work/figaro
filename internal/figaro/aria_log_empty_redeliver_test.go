package figaro_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/uiir"
	"github.com/stretchr/testify/require"
)

func TestAnEmptyEntrySurvivesARestartThatRedeliversIt(t *testing.T) {
	birth := form.Build(form.Snapshot{}, map[string]json.RawMessage{form.KindKey: json.RawMessage(`"log"`)}, nil)
	be, id := store.NewTestAria(t, "d", birth)
	dir := filepath.Join(t.TempDir(), "spool")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	raw, err := json.Marshal(map[string]any{"queued": []map[string]any{{
		"segments": []map[string]any{{"principal": "user:1000"}},
		"at":       time.Now().UnixMilli(),
		"keys":     []string{"never-appended"},
		"entry":    true,
	}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600))

	cb, _ := form.Open("")
	cb.Apply(birth)
	a := figaro.NewAgent(figaro.Config{Projector: uiir.New(nil), ID: id, Backend: be, Form: cb, Spool: figaro.NewSpool(dir)})
	a.Redeliver()
	defer a.Kill()

	require.Eventually(t, func() bool {
		for _, m := range a.Context() {
			if len(m.Accepted) > 0 {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
	var entries int
	for _, m := range a.Context() {
		if message.IsEntry(m) {
			entries++
		}
	}
	require.Equal(t, 1, entries, "the empty entry the spool held lands as an entry, not as a content-less message")
}
