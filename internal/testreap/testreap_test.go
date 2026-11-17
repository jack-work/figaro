//go:build linux

package testreap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	o := Start()
	os.Exit(o.Check(m.Run()))
}

const helperEnv = "TESTREAP_HELPER_PIDFILE"

func detachedSleep(t *testing.T, marker string) {
	t.Helper()
	c := exec.Command("sh", "-c", "sleep 300 & echo $! >\"$0\"; exec sleep 300", os.Getenv(helperEnv))
	c.Env = append(os.Environ(), marker)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Wait() }()
	pidfile := os.Getenv(helperEnv)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if raw, err := os.ReadFile(pidfile); err == nil && strings.HasSuffix(string(raw), "\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper's background sleep never wrote its pid")
		}
	}
	if err := os.WriteFile(pidfile+".leader", []byte(strconv.Itoa(c.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHelperLeaksPastItsScope(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("run by TestAScopeFailsTheTestThatLeaks")
	}
	detachedSleep(t, Scope(t))
}

func TestHelperLeaksPastThePackage(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("run by TestAPackageThatLeaksFails")
	}
	detachedSleep(t, Env())
	Grace = time.Second
}

func TestHelperHangsUntilTheTimeout(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("run by TestATimedOutBinaryIsReaped")
	}
	detachedSleep(t, Scope(t))
	time.Sleep(time.Hour)
}

func runHelper(t *testing.T, name string, extra ...string) (string, []int) {
	t.Helper()
	pidfile := filepath.Join(t.TempDir(), "pids")
	args := append([]string{"-test.run=^" + name + "$", "-test.count=1"}, extra...)
	c := exec.Command(os.Args[0], args...)
	c.Env = append(os.Environ(), helperEnv+"="+pidfile)
	out, _ := c.CombinedOutput()
	var pids []int
	for _, f := range []string{pidfile, pidfile + ".leader"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s wrote no pid: %v\n%s", name, err, out)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		pids = append(pids, pid)
	}
	return string(out), pids
}

func requireGone(t *testing.T, pids []int, out string) {
	t.Helper()
	for _, pid := range pids {
		if st, ok := readStat(pid); ok && st.state != "Z" && strings.Contains(cmdline(pid), "sleep 300") {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("pid %d outlived the helper binary\n%s", pid, out)
		}
	}
}

func TestAScopeFailsTheTestThatLeaks(t *testing.T) {
	out, pids := runHelper(t, "TestHelperLeaksPastItsScope")
	if !strings.Contains(out, "outlived TestHelperLeaksPastItsScope") || !strings.Contains(out, "FAIL") {
		t.Errorf("a leak past the scope must fail the test that made it:\n%s", out)
	}
	requireGone(t, pids, out)
}

func TestAPackageThatLeaksFails(t *testing.T) {
	out, pids := runHelper(t, "TestHelperLeaksPastThePackage")
	if !strings.Contains(out, "outlived every test in this package") {
		t.Errorf("a leak past the package must be reported:\n%s", out)
	}
	requireGone(t, pids, out)
}

func TestATimedOutBinaryIsReaped(t *testing.T) {
	out, pids := runHelper(t, "TestHelperHangsUntilTheTimeout", "-test.timeout=3s")
	if !strings.Contains(out, "panic: test timed out") {
		t.Fatalf("the helper was meant to time out:\n%s", out)
	}
	if !strings.Contains(out, "the test binary ended without its cleanups") {
		t.Errorf("the reaper must say what it ended:\n%s", out)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		alive := 0
		for _, pid := range pids {
			if st, ok := readStat(pid); ok && st.state != "Z" {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		if time.Now().After(deadline) {
			requireGone(t, pids, out)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
