//go:build !windows

package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/creack/pty"
)

// A session is interactive only on a terminal it may read and paint: a file
// on stdout or a terminal that is not ours to control means no raw mode, no
// pager, and an exit when the turn ends.

func TestInteractiveRefusesAFileOnStdout(t *testing.T) {
	_, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if Interactive(tty, f) {
		t.Fatal("a terminal on stdin and a file on stdout was called interactive")
	}
}

func TestInteractiveRefusesATerminalThatIsNotOurs(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	if Interactive(tty, tty) {
		t.Fatal("a terminal whose foreground group is not ours was called interactive")
	}
}
