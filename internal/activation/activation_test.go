//go:build !windows

package activation

import (
	"fmt"
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
	get, unset, env := envOf(map[string]string{envPID: "99", envFDs: "1", envNames: "angelus"})
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
	if os.Getenv("ACTIVATION_HELPER") == "" {
		t.Skip("child half of TestNamedListenersReachAChild")
	}
	s, err := TakeEnv()
	if err != nil {
		fmt.Println("take:", err)
		os.Exit(2)
	}
	fmt.Println("names", strings.Join(s.Names(), ","), "env", os.Getenv(envFDs) == "" && os.Getenv(envPID) == "" && os.Getenv(envNames) == "")
	for _, name := range s.Names() {
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
	fmt.Println("done")
	os.Exit(0)
}

func TestNamedListenersReachAChild(t *testing.T) {
	dir := t.TempDir()
	names := []string{"angelus", "web"}
	var files []*os.File
	paths := map[string]string{}
	for _, n := range names {
		p := filepath.Join(dir, n+".sock")
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := ln.(*net.UnixListener).File()
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		defer ln.Close()
		files = append(files, f)
		paths[n] = p
	}
	cmd := exec.Command("sh", "-c", `LISTEN_PID=$$ exec "$0" -test.run=^TestHelperProcess$`, os.Args[0])
	cmd.Env = append(os.Environ(), "ACTIVATION_HELPER=1", "LISTEN_FDS=2", "LISTEN_FDNAMES=angelus:web")
	cmd.ExtraFiles = files
	out := &strings.Builder{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
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
	if !strings.Contains(out.String(), "names angelus,web env true") {
		t.Fatalf("child saw: %s", out)
	}
}

func TestListenerIsHandedOverOnce(t *testing.T) {
	s := &Sockets{files: map[string]*os.File{}}
	if _, ok, err := s.Listener("angelus"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
