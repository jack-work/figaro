package angelus

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

type countingRetentionBackend struct {
	store.Backend
}

func (countingRetentionBackend) RetainFormsCounted(context.Context, bool, time.Duration) ([]store.FormRetention, int, error) {
	return []store.FormRetention{{ID: "a", Dropped: 2}, {ID: "b", Dropped: 3}}, 11, nil
}

func (countingRetentionBackend) SweepQueueEntries(time.Time, time.Duration) (int, error) {
	return 4, nil
}

func TestARetentionPassLogsOneInfoLineWithItsCounts(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[store]\ngc = \"on\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Backend: countingRetentionBackend{}, Settings: loaded}
	a.runRetentionPass(context.Background())

	var passes []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, `msg="retention pass"`) {
			passes = append(passes, l)
		}
	}
	if len(passes) != 1 {
		t.Fatalf("want one retention pass line, got %d in:\n%s", len(passes), buf.String())
	}
	for _, want := range []string{"forms_scanned=11", "segments_dropped=5", "queue_entries_deleted=4", "mode=on", "level=INFO"} {
		if !strings.Contains(passes[0], want) {
			t.Fatalf("%q missing from %s", want, passes[0])
		}
	}
}
