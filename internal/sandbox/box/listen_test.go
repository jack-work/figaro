package box

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const inheritHelperEnv = "FIGARO_TEST_INHERIT_HELPER"

func TestInheritHelper(t *testing.T) {
	if os.Getenv(inheritHelperEnv) == "" {
		t.Skip("run by TestBoxServeTakesTheListenerASocketUnitPasses")
	}
	ln, passed, err := Inherited()
	if err != nil {
		os.Stdout.WriteString("err: " + err.Error() + "\n")
		os.Exit(0)
	}
	if !passed {
		os.Stdout.WriteString("none\n")
		os.Exit(0)
	}
	left := os.Getenv("LISTEN_FDS") + os.Getenv("LISTEN_PID") + os.Getenv("LISTEN_FDNAMES")
	os.Stdout.WriteString("passed left=" + left + "\n")
	c, err := ln.Accept()
	if err != nil {
		os.Exit(3)
	}
	c.Write([]byte("served\n"))
	c.Close()
	os.Exit(0)
}

func runInheritHelper(t *testing.T, fds, pid string, pass bool) (string, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("", "boxln")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	env := []string{inheritHelperEnv + "=1"}
	if fds != "" {
		env = append(env, "LISTEN_FDS="+fds)
	}
	script := `exec "$0" -test.run '^TestInheritHelper$'`
	switch pid {
	case "self":
		script = `LISTEN_PID=$$ ` + script
	case "":
	default:
		script = `LISTEN_PID=` + pid + ` ` + script
	}
	cmd := exec.Command("/bin/sh", "-c", script, os.Args[0])
	cmd.Env = append(os.Environ(), env...)
	if pass {
		f, err := ln.(*net.UnixListener).File()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.ExtraFiles = []*os.File{f}
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, _ := bufio.NewReader(out).ReadString('\n')
	return strings.TrimSpace(line), ln
}

func TestBoxServeTakesTheListenerASocketUnitPasses(t *testing.T) {
	line, ln := runInheritHelper(t, "1", "self", true)
	if line != "passed left=" {
		t.Fatalf("helper said %q, want the passed listener taken and LISTEN_* cleared from its environment", line)
	}
	c, err := net.DialTimeout("unix", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := bufio.NewReader(c).ReadString('\n')
	if got != "served\n" {
		t.Fatalf("a connection to the passed socket got %q, want the helper's answer", got)
	}
}

func TestBoxServeRefusesAListenerPassedToAnotherProcess(t *testing.T) {
	line, _ := runInheritHelper(t, "1", "1", true)
	if !strings.HasPrefix(line, "err: ") || !strings.Contains(line, "someone else's") {
		t.Fatalf("helper said %q, want a refusal naming LISTEN_PID", line)
	}
}

func TestBoxServeRefusesMoreThanOnePassedListener(t *testing.T) {
	line, _ := runInheritHelper(t, "2", "self", true)
	if !strings.HasPrefix(line, "err: ") || !strings.Contains(line, "exactly one") {
		t.Fatalf("helper said %q, want a refusal of two listeners", line)
	}
}

func TestBoxServeWithNothingPassedBindsItsOwnSocket(t *testing.T) {
	line, _ := runInheritHelper(t, "", "", false)
	if line != "none" {
		t.Fatalf("helper said %q, want no passed listener", line)
	}
}
