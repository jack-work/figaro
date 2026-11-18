//go:build !windows

package activation

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) (func(string) string, func(string) error, map[string]string) {
	return func(k string) string { return m[k] }, func(k string) error { delete(m, k); return nil }, m
}

func TestAbsentVariablesMeanNotActivated(t *testing.T) {
	get, unset, _ := envOf(map[string]string{})
	s, err := Take(get, unset, 10)
	if err != nil || s.Active() {
		t.Fatalf("active=%v err=%v", s.Active(), err)
	}
}

func TestAnotherProcessesSocketsAreLeftAlone(t *testing.T) {
	get, unset, env := envOf(map[string]string{envPID: "99", envFDs: "1", envNames: "angelus", envPidfdID: "7"})
	s, err := Take(get, unset, 10)
	if err != nil || s.Active() {
		t.Fatalf("active=%v err=%v", s.Active(), err)
	}
	if len(env) != 0 {
		t.Fatalf("the variables outlived the claim: %v", env)
	}
}

func TestMalformedVariablesAreRefused(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"pid":   {envPID: "x", envFDs: "1"},
		"count": {envPID: "10", envFDs: "-1"},
		"text":  {envPID: "10", envFDs: "many"},
	} {
		get, unset, _ := envOf(env)
		if _, err := Take(get, unset, 10); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestZeroSocketsIsActiveButEmpty(t *testing.T) {
	get, unset, _ := envOf(map[string]string{envPID: "10", envFDs: "0"})
	s, err := Take(get, unset, 10)
	if err != nil || !s.Active() || len(s.Names()) != 0 {
		t.Fatalf("active=%v names=%v err=%v", s.Active(), s.Names(), err)
	}
}

func TestHelperProcess(t *testing.T) {
	switch os.Getenv("ACTIVATION_HELPER") {
	case "":
		t.Skip("child half of activation helper tests")
	case "duplicate":
		_, err := TakeEnv()
		if err == nil {
			fmt.Println("accepted duplicate")
			os.Exit(2)
		}
		for fd := firstFD; fd < firstFD+3; fd++ {
			if _, serr := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); serr == nil {
				fmt.Printf("fd %d stayed open\n", fd)
				os.Exit(2)
			}
		}
		fmt.Println("closed-all")
		os.Exit(0)
	}
	s, err := TakeEnv()
	if err != nil {
		fmt.Println("take:", err)
		os.Exit(2)
	}
	cleanEnv := os.Getenv(envFDs) == "" && os.Getenv(envPID) == "" && os.Getenv(envNames) == "" && os.Getenv(envPidfdID) == ""
	fmt.Println("names", strings.Join(s.Names(), ","), "env", cleanEnv)
	for _, name := range []string{"angelus", "web"} {
		ln, _, err := s.Listener(name)
		if err != nil {
			fmt.Println("listener:", err)
			os.Exit(2)
		}
		c, err := ln.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			os.Exit(2)
		}
		fmt.Fprintf(c, "hello from %s\n", name)
		c.Close()
		ln.Close()
	}
	if err := s.Close(); err != nil {
		fmt.Println("close:", err)
		os.Exit(2)
	}
	if _, serr := unix.FcntlInt(uintptr(firstFD+2), unix.F_GETFD, 0); serr == nil {
		fmt.Println("unserved fd stayed open after Close")
		os.Exit(2)
	}
	fmt.Println("done")
	os.Exit(0)
}

func TestNamedListenersReachAChildAndUnservedOnesClose(t *testing.T) {
	dir := t.TempDir()
	names := []string{"angelus", "web", "extra"}
	var files []*os.File
	paths := map[string]string{}
	for _, n := range names {
		p := filepath.Join(dir, n+".sock")
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		ul := ln.(*net.UnixListener)
		ul.SetUnlinkOnClose(false)
		f, err := ul.File()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		ln.Close()
		files = append(files, f)
		paths[n] = p
	}
	cmd := exec.Command("sh", "-c", `LISTEN_PID=$$ exec "$0" -test.run=^TestHelperProcess$`, os.Args[0])
	cmd.Env = append(os.Environ(), "ACTIVATION_HELPER=1", "LISTEN_FDS=3", "LISTEN_FDNAMES=angelus:web:extra", "LISTEN_PIDFDID=42")
	cmd.ExtraFiles = files
	out := &strings.Builder{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"angelus", "web"} {
		c, err := net.DialTimeout("unix", paths[n], 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		k, _ := c.Read(buf)
		c.Close()
		if got := string(buf[:k]); got != "hello from "+n+"\n" {
			t.Fatalf("%s answered %q\n%s", n, got, out)
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "names angelus,extra,web env true") {
		t.Fatalf("child saw: %s", out)
	}
	for _, n := range names {
		if _, err := os.Stat(paths[n]); err != nil {
			t.Fatalf("closing inherited listener unlinked %s: %v", n, err)
		}
	}
}

func TestDuplicateNameClosesEveryPassedFD(t *testing.T) {
	var files []*os.File
	for i := 0; i < 3; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		files = append(files, r)
	}
	cmd := exec.Command("sh", "-c", `LISTEN_PID=$$ exec "$0" -test.run=^TestHelperProcess$`, os.Args[0])
	cmd.Env = append(os.Environ(), "ACTIVATION_HELPER=duplicate", "LISTEN_FDS=3", "LISTEN_FDNAMES=angelus:angelus:web")
	cmd.ExtraFiles = files
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "closed-all") {
		t.Fatalf("duplicate cleanup failed: %v\n%s", err, out)
	}
}

func TestListenerIsHandedOverOnce(t *testing.T) {
	s := &Sockets{files: map[string]*os.File{}}
	if _, ok, err := s.Listener("angelus"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestAcceptedConnectionIsHandedOverOnce(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	peer := os.NewFile(uintptr(fds[1]), "peer")
	defer peer.Close()
	s := &Sockets{files: map[string]*os.File{Accepted: os.NewFile(uintptr(fds[0]), Accepted)}}
	c, ok, err := s.Conn(Accepted)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	k, _ := peer.Read(buf)
	if string(buf[:k]) != "hi\n" {
		t.Fatalf("peer read %q", buf[:k])
	}
	if _, ok, _ := s.Conn(Accepted); ok {
		t.Fatal("the connection was handed over twice")
	}
}
