package cli

import (
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

func TestParseUnitShow(t *testing.T) {
	u := parseUnitShow("Id=figaro.service\nLoadState=loaded\nActiveState=active\nMainPID=4242\n")
	if u.Name != "figaro.service" || u.Load != "loaded" || u.Active != "active" || u.MainPID != 4242 {
		t.Fatalf("%+v", u)
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
