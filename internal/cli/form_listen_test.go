package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/form"
)

// paintTo runs one paint against a temp file and returns what was written.
func paintTo(t *testing.T, v *formView) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	v.out = f
	v.paint()
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(body)
}

func testView() *formView {
	mirror := &formMirror{}
	mirror.reset(form.Snapshot{}, 1)
	return &formView{mirror: mirror, open: map[string]bool{}, aria: "@test"}
}

// TestPaintIsSilentUntilTheAlternateScreenIsOn is the regression test for the
// bug this file exists to prevent: quitting `fig form listen` left the form's
// rows and a screenful of blank lines where the user's terminal had been.
//
// paint() opens with erase-in-display and closes by parking the cursor on the
// last row. On the alternate screen that is free. On the PRIMARY screen it
// erases what the user was reading -- and since the alternate screen is put
// back on exit, that wreckage is exactly what they are left looking at.
//
// Two paints could reach the primary screen. The seeding resync ran before the
// terminal was switched, which made the damage deterministic; and a delta can
// arrive on the notifier's goroutine at any time, including after teardown has
// restored the screen but before the process has exited.
func TestPaintIsSilentUntilTheAlternateScreenIsOn(t *testing.T) {
	v := testView()

	if got := paintTo(t, v); got != "" {
		t.Fatalf("painted %d bytes before the alternate screen was on: %q", len(got), got)
	}

	v.begin()
	live := paintTo(t, v)
	if live == "" {
		t.Fatal("painted nothing once the alternate screen was on")
	}
	if !strings.Contains(live, "\x1b[2J") {
		t.Fatalf("a live paint does not clear: %q", live)
	}

	// And silent again on the way out, BEFORE the screen is handed back.
	v.end()
	if got := paintTo(t, v); got != "" {
		t.Fatalf("painted %d bytes during teardown: %q", len(got), got)
	}
}

// A resync arriving before the alternate screen is on must not paint either:
// it is the path that made the bug deterministic rather than racy.
func TestResyncDoesNotPaintBeforeTheScreenIsSwitched(t *testing.T) {
	v := testView()
	v.refetch = func() (form.Snapshot, uint64, error) { return form.Snapshot{}, 2, nil }

	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	v.out = f
	v.resync()
	f.Close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("the seeding resync wrote %d bytes to the user's terminal: %q", len(body), body)
	}
}
