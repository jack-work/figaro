package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/boot"
)

func TestBootReasonPrefersTheNamedReasonThenTheSpawner(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{boot.BootReasonEnv: "upgrade", "_FIGARO_DAEMON": "1", "INVOCATION_ID": "x"}, "upgrade"},
		{map[string]string{"_FIGARO_DAEMON": "1", "INVOCATION_ID": "x"}, "cli"},
		{map[string]string{"INVOCATION_ID": "x"}, "unit"},
		{map[string]string{}, "direct"},
	} {
		if got := bootReason(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("env %v: got %q, want %q", c.env, got, c.want)
		}
	}
}

// The stamp is taken once: a crash before the next graceful stop must not
// leave the boot after it claiming a restart gap.
func TestStopStampIsReadOnceAndMeasuredFromStart(t *testing.T) {
	dir := t.TempDir()
	stopped := time.UnixMilli(1_700_000_000_000)
	if err := os.WriteFile(boot.StopStampPath(dir), []byte(strconv.FormatInt(stopped.UnixMilli(), 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gap, ok := takeStopStamp(dir, stopped.Add(1500*time.Millisecond))
	if !ok || gap != 1500*time.Millisecond {
		t.Fatalf("got %v %v, want 1.5s true", gap, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "angelus.stopped")); !os.IsNotExist(err) {
		t.Fatalf("stamp still there: %v", err)
	}
	if _, ok := takeStopStamp(dir, time.Now()); ok {
		t.Fatal("a second boot read the same stamp")
	}
}
