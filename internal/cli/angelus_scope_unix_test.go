//go:build !windows

package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// El Inspector XII: an inherited XDG_RUNTIME_DIR can point at a directory that
// is not ours (someone else's, a container bind). The daemon is handed only to
// a manager whose socket sits in a directory we own and nobody else can write.

func managerDir(t *testing.T, mode os.FileMode) (dir, socket string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "systemd")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket = filepath.Join(dir, "private")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir, socket
}

func TestAManagerInADirectoryWeOwnAloneIsUsed(t *testing.T) {
	_, socket := managerDir(t, 0o700)
	if !ownManagerSocket(socket) {
		t.Fatal("a manager socket in our own 0700 directory was refused")
	}
}

func TestAManagerInADirectoryOthersCanWriteIsRefused(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"group-writable": 0o770, "world-writable": 0o707} {
		t.Run(name, func(t *testing.T) {
			_, socket := managerDir(t, mode)
			if ownManagerSocket(socket) {
				t.Fatalf("a manager socket in a %s directory (%o) was used", name, mode)
			}
		})
	}
}

func TestAManagerInADirectoryAnotherUserOwnsIsRefused(t *testing.T) {
	dir, _ := managerDir(t, 0o700)
	if trustedManagerDir(dir, os.Getuid()+1) {
		t.Fatal("a directory owned by another uid was trusted")
	}
}

func TestAManagerReachedThroughASymlinkIsRefused(t *testing.T) {
	dir, _ := managerDir(t, 0o700)
	link := filepath.Join(t.TempDir(), "systemd")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if ownManagerSocket(filepath.Join(link, "private")) {
		t.Fatal("a manager directory reached through a symlink was trusted")
	}
}
