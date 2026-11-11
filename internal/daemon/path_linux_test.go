package daemon

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyASocketOwnedByUsIsTheUserManager(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "private")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !ownedSocket(sock, os.Getuid()) {
		t.Fatal("our own socket was refused")
	}
	if ownedSocket(sock, os.Getuid()+1) {
		t.Fatal("a socket owned by another uid was taken as the user manager")
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if ownedSocket(file, os.Getuid()) {
		t.Fatal("a regular file was taken as the user manager")
	}
}

func TestNoManagerSocketIsNoUserManager(t *testing.T) {
	if _, err := userManagerPATH(t.TempDir()); !errors.Is(err, errNoUserManager) {
		t.Fatalf("err = %v, want errNoUserManager", err)
	}
}

func TestTheUserManagersPathIsRead(t *testing.T) {
	xdg := os.Getenv("XDG_RUNTIME_DIR")
	if xdg == "" || !ownedSocket(filepath.Join(xdg, "systemd", "private"), os.Getuid()) {
		t.Skip("no user manager here")
	}
	out, err := exec.Command("systemctl", "--user", "show-environment").Output()
	if err != nil {
		t.Skipf("systemctl: %v", err)
	}
	want := pathIn(strings.Split(string(out), "\n"))
	got, err := userManagerPATH(xdg)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || got == "" {
		t.Fatalf("manager PATH over D-Bus = %q, systemctl says %q", got, want)
	}
}
