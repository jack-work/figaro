package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func graceFrom(t *testing.T, toml string) time.Duration {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return l.QueueEntryGrace()
}

func TestQueueGraceIsSevenDaysUnlessTheStoreSectionSaysOtherwise(t *testing.T) {
	if got := graceFrom(t, ""); got != 7*24*time.Hour {
		t.Fatalf("default %v", got)
	}
	if got := graceFrom(t, "[store]\nqueue_grace_days = 2\n"); got != 48*time.Hour {
		t.Fatalf("two days: %v", got)
	}
	if got := graceFrom(t, "[store]\nqueue_grace_days = 0\n"); got != 0 {
		t.Fatalf("zero keeps entries: %v", got)
	}
}
