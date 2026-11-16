package telemetrydir

import (
	"os"
	"path/filepath"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestForKeepsWritableDir(t *testing.T) {
	want := filepath.Join(t.TempDir(), "state")
	xdg := t.TempDir()
	if got := For(want, envOf(map[string]string{"XDG_STATE_HOME": xdg})); got != want {
		t.Fatalf("writable dir: got %q, want %q", got, want)
	}
}

func TestForFallsBackWhenDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through mode bits")
	}
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shared, 0o700) })
	xdg := t.TempDir()
	got := For(shared, envOf(map[string]string{"XDG_STATE_HOME": xdg}))
	if want := filepath.Join(xdg, "figaro"); got != want {
		t.Fatalf("unwritable dir: got %q, want %q", got, want)
	}
}

func TestForFallsBackWhenTraceFileUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through mode bits")
	}
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "traces.jsonl"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	got := For(shared, envOf(map[string]string{"HOME": home}))
	if want := filepath.Join(home, ".local", "state", "figaro"); got != want {
		t.Fatalf("read-only traces.jsonl: got %q, want %q", got, want)
	}
}

func TestForKeepsWantWhenNoFallbackWorks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through mode bits")
	}
	shared := filepath.Join(t.TempDir(), "shared")
	os.MkdirAll(shared, 0o700)
	os.Chmod(shared, 0o500)
	t.Cleanup(func() { os.Chmod(shared, 0o700) })
	got := For(shared, envOf(map[string]string{"XDG_STATE_HOME": shared}))
	if got != shared {
		t.Fatalf("no usable fallback: got %q, want %q (otel reports the error)", got, shared)
	}
}
