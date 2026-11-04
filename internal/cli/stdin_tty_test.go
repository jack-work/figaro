package cli

import (
	"os"
	"testing"
)

// Fixes #65: /dev/null is a character device, and a service manager hands it
// to figaro as stdin; nobody can answer a prompt there.
func TestStdinFromDevNullIsNotATerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	saved := os.Stdin
	os.Stdin = null
	defer func() { os.Stdin = saved }()
	if isStdinTTY() {
		t.Fatal("stdin from /dev/null reads as a terminal")
	}
}
