package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

// Gluck, 0.56.3: ':version' in the pager wrote its five lines raw over the
// bottom of the screen and the bar said "version: ok"; the next key painted
// them away for good. A verb run from the pager owns no terminal: every byte
// it prints must land in what routeCaptured hands the pager, and none on the
// process's own stdout or stderr, which are the pager's screen.
func TestAVerbRunFromThePagerWritesNothingToTheTerminal(t *testing.T) {
	in, _ := navInput(t, &countingWriter{}, true)
	t.Setenv("FIGARO_RUNTIME_DIR", t.TempDir())
	for _, argv := range [][]string{
		{"version"},
		{"version", "-j"},
		{"help"},
		{"status"},
		{"ls", "-j"},
		{"doctor", "term"},
		{"doctor", "unit"},
		{"doctor", "unit", "-j"},
		{"doctor", "ttl", "-j"},
		{"doctor", "keys"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			screen := grabTerminal(t)
			out, code := in.routeCaptured(argv)
			leaked := screen()
			if leaked != "" {
				t.Fatalf("%q wrote %d bytes to the terminal under the pager:\n%s", argv, len(leaked), leaked)
			}
			if strings.TrimSpace(out.text) == "" && code == 0 {
				t.Fatalf("%q printed nothing the pager could show", argv)
			}
		})
	}
}

func grabTerminal(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevOut, prevErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		got <- string(b)
	}()
	return func() string {
		os.Stdout, os.Stderr = prevOut, prevErr
		w.Close()
		s := <-got
		r.Close()
		return s
	}
}
