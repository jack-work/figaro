package cli

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTheSocketWatchFiresWhenTheSocketIsBound(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "angelus.sock")
	created, stop := watchSocketCreated(sock)
	defer stop()
	if created == nil {
		t.Fatal("no watch on a directory that exists")
	}
	if err := os.WriteFile(filepath.Join(dir, "angelus.pid"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created:
		t.Fatal("the watch fired for a file that is not the socket")
	case <-time.After(50 * time.Millisecond):
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("binding the socket did not fire the watch")
	}
}

func TestTheSocketWatchFiresForASocketRenamedIntoPlace(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "angelus.sock")
	created, stop := watchSocketCreated(sock)
	defer stop()
	if err := os.WriteFile(sock+".tmp", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sock+".tmp", sock); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("a rename into place did not fire the watch")
	}
}

func TestAWatchOnAMissingDirectoryFallsBackToTheTimer(t *testing.T) {
	created, stop := watchSocketCreated(filepath.Join(t.TempDir(), "gone", "angelus.sock"))
	defer stop()
	if created != nil {
		t.Fatal("a watch on a missing directory returned a channel that can never fire")
	}
}

// inotify reports the socket at bind(), before listen(): the first dial can
// be refused, and the retry after it must not wait out a long backoff step.
func TestABoundSocketRestartsTheBackoffShort(t *testing.T) {
	if got := nextReconnectWait(1600*time.Millisecond, true); got != reconnectAfterBind {
		t.Fatalf("after the socket appeared the next retry waits %s, want %s", got, reconnectAfterBind)
	}
	if got := nextReconnectWait(reconnectAfterBind, false); got != 2*reconnectAfterBind {
		t.Fatalf("the backoff after a bind does not double: %s", got)
	}
	if got := nextReconnectWait(reconnectMax, false); got != reconnectMax {
		t.Fatalf("the backoff passed its cap: %s", got)
	}
}
