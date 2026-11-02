package upstream

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

const netnsChild = "UPSTREAM_NETNS_CHILD"

// The kernel half: an address added in a private network namespace must
// reach the watcher. Skips where user namespaces or iproute2 are missing.
func TestTheNetlinkWatcherHearsAnAddressChange(t *testing.T) {
	if os.Getenv(netnsChild) == "1" {
		netnsChildMain(t)
		return
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("no ip(8) to change an address with")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheNetlinkWatcherHearsAnAddressChange$", "-test.v")
	cmd.Env = append(os.Environ(), netnsChild+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Skipf("no user and network namespace here: %v", err)
	}
	if err != nil {
		t.Fatalf("netns child failed:\n%s", out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("netns child skipped:\n%s", out)
	}
}

func netnsChildMain(t *testing.T) {
	heard := make(chan struct{}, 1)
	if err := watchRoutes(func() {
		select {
		case heard <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	if out, err := exec.Command("ip", "addr", "add", "10.9.9.9/32", "dev", "lo").CombinedOutput(); err != nil {
		t.Skipf("ip addr add: %v: %s", err, out)
	}
	select {
	case <-heard:
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher heard nothing after an address was added")
	}
}
