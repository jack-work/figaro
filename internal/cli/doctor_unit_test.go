package cli

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Fixes #60 (its doctor ask): a daemon a client spawned while the unit was
// restarting must be visible, not found by comparing pids by hand.
func TestJudgeDaemonUnit(t *testing.T) {
	active := &daemonUnit{Name: "figaro.service", Scope: "user", Load: "loaded", Active: "active", MainPID: 100}
	inactive := &daemonUnit{Name: "figaro.service", Scope: "user", Load: "loaded", Active: "inactive"}
	for _, tc := range []struct {
		name   string
		unit   *daemonUnit
		owner  int
		ok     bool
		phrase string
	}{
		{"the unit owns the socket", active, 100, true, "owns the socket"},
		{"another pid answers", active, 200, false, "not the unit's"},
		{"the unit is down and a client's daemon answers", inactive, 200, false, "a client spawned it"},
		{"the unit is up and nothing answers", active, 0, false, "nothing answers"},
		{"no unit", nil, 300, true, "no daemon unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := judgeDaemonUnit(tc.unit, tc.owner)
			if v.OK != tc.ok || !strings.Contains(v.Message, tc.phrase) {
				t.Fatalf("ok=%v %q, want ok=%v containing %q", v.OK, v.Message, tc.ok, tc.phrase)
			}
		})
	}
}

// Defends F4: under socket activation, an idle unit must not be dialed (which
// wakes it), and an active unit whose socket is held by systemd is verified
// against angelus.pid.
func TestInspectSocketActivatedUnit(t *testing.T) {
	sock := "/run/figaro/angelus.sock"
	su := &socketUnit{Name: "figaro.socket", Scope: "system", Load: "loaded", Active: "active", Sub: "listening", Listen: []string{sock}}
	idle := &daemonUnit{Name: "figaro.service", Scope: "system", Load: "loaded", Active: "inactive", TriggeredBy: []string{"figaro.socket"}}
	active := &daemonUnit{Name: "figaro.service", Scope: "system", Load: "loaded", Active: "active", Sub: "running", MainPID: 4242, TriggeredBy: []string{"figaro.socket"}}

	t.Run("idle socket unit is ok without dialing", func(t *testing.T) {
		dialed := false
		v := inspectDaemonUnit(idle, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { dialed = true; return 1, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 0, fs.ErrNotExist },
			alive:     func(int) bool { return false },
			isManager: func(string, int) bool { return true },
		})
		if dialed {
			t.Fatal("doctor unit dialed an idle manager-held socket")
		}
		if !v.OK || !strings.Contains(v.Message, "figaro.socket holds the socket") {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("idle service with stray live pid in angelus.pid warns", func(t *testing.T) {
		v := inspectDaemonUnit(idle, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { t.Fatal("must not dial"); return 0, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 9000, nil },
			alive:     func(pid int) bool { return pid == 9000 },
			isManager: func(string, int) bool { return true },
		})
		if v.OK || !strings.Contains(v.Message, "outside it") {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("active service with manager-held socket and matching angelus.pid is ok", func(t *testing.T) {
		v := inspectDaemonUnit(active, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { return 1, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 4242, nil },
			alive:     func(pid int) bool { return pid == 4242 },
			isManager: func(scope string, pid int) bool { return scope == "system" && pid == 1 },
		})
		if !v.OK || !strings.Contains(v.Message, "serves figaro.socket's socket") {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("active service with mismatched angelus.pid warns", func(t *testing.T) {
		v := inspectDaemonUnit(active, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { return 1, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 7777, nil },
			alive:     func(int) bool { return true },
			isManager: func(scope string, pid int) bool { return scope == "system" && pid == 1 },
		})
		if v.OK || !strings.Contains(v.Message, "not the unit's") {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("active service whose socket was rebound by a non-manager pid warns", func(t *testing.T) {
		v := inspectDaemonUnit(active, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { return 8888, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 4242, nil },
			alive:     func(int) bool { return true },
			isManager: func(scope string, pid int) bool { return scope == "system" && pid == 1 },
		})
		if v.OK || !strings.Contains(v.Message, "not the unit's") {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("active service with EACCES on angelus.pid still trusts manager-held socket", func(t *testing.T) {
		v := inspectDaemonUnit(active, su, sock, unitProbes{
			ownerPID:  func(string) (int, error) { return 1, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 0, fs.ErrPermission },
			alive:     func(pid int) bool { return pid == 4242 },
			isManager: func(scope string, pid int) bool { return scope == "system" && pid == 1 },
		})
		if !v.OK {
			t.Fatalf("verdict: %+v", v)
		}
	})

	t.Run("isolated runtime dir ignores unrelated system socket unit", func(t *testing.T) {
		v := inspectDaemonUnit(active, su, "/tmp/iso/angelus.sock", unitProbes{
			ownerPID:  func(string) (int, error) { return 5555, nil },
			listening: func(string) (bool, bool) { return true, true },
			pidFile:   func() (int, error) { return 4242, nil },
			alive:     func(int) bool { return true },
			isManager: func(string, int) bool { return true },
		})
		if v.OK || !strings.Contains(v.Message, "not the unit's") {
			t.Fatalf("verdict: %+v", v)
		}
	})
}

func TestParseUnitShow(t *testing.T) {
	u := parseUnitShow("Id=figaro.service\nLoadState=loaded\nActiveState=active\nMainPID=4242\nTriggeredBy=figaro.socket\n")
	if u.Name != "figaro.service" || u.Load != "loaded" || u.Active != "active" || u.MainPID != 4242 || len(u.TriggeredBy) != 1 || u.TriggeredBy[0] != "figaro.socket" {
		t.Fatalf("%+v", u)
	}
	s := parseSocketShow("Id=figaro.socket\nLoadState=loaded\nActiveState=active\nSubState=listening\nListen=/run/figaro/angelus.sock (Stream)\n")
	if s.Name != "figaro.socket" || !s.holds("/run/figaro/angelus.sock") || s.holds("/tmp/other.sock") {
		t.Fatalf("%+v", s)
	}
}

func TestSocketOwnerPIDIsTheListener(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("peercred pid is linux only")
	}
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	pid, err := socketOwnerPID(path)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("pid %d err %v, want %d", pid, err, os.Getpid())
	}
}

func TestPidAliveRecognizesAnotherUIDsProcess(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs non-root unix EPERM on pid 1")
	}
	if !pidAlive(1) {
		t.Fatal("pid 1 reported dead when kill(1, 0) returned EPERM")
	}
}
