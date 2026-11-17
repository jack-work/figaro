package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// #60: while a unit restarts its daemon, a client that found no socket spawned
// its own; the unit's ExecCondition then saw that daemon and left the unit
// inactive. A client must defer to a unit that serves its runtime dir.

func TestUnitGovernsOnlyItsOwnRuntimeDir(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	user := env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"})
	for _, tc := range []struct {
		name string
		unit *daemonUnit
		ours string
		want bool
	}{
		{"no unit", nil, "/run/user/1000/figaro", false},
		{"user unit on the default dir", &daemonUnit{Scope: "user"}, "/run/user/1000/figaro", true},
		{"user unit, client on an isolated dir", &daemonUnit{Scope: "user"}, "/tmp/iso/run", false},
		{"unit pinned to the client's dir", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/iso/run/"}}, "/tmp/iso/run", true},
		{"unit pinned elsewhere", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/other"}}, "/run/user/1000/figaro", false},
		{"system unit with its own xdg dir", &daemonUnit{Scope: "system", Env: []string{"XDG_RUNTIME_DIR=/run/figaro-x"}}, "/run/figaro-x/figaro", true},
		{"system unit naming no dir", &daemonUnit{Scope: "system"}, "/run/user/1000/figaro", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unitGoverns(tc.unit, tc.ours, user); got != tc.want {
				t.Fatalf("unitGoverns = %v, want %v", got, tc.want)
			}
		})
	}
}

type fakeUnit struct {
	states  []daemonUnit
	starts  int
	answer  int
	dials   int
	startOK bool
}

func (f *fakeUnit) watch(say *bytes.Buffer) unitWatch {
	return unitWatch{
		show: func() *daemonUnit {
			u := f.states[0]
			if len(f.states) > 1 {
				f.states = f.states[1:]
			}
			return &u
		},
		start: func(*daemonUnit) error {
			f.starts++
			if !f.startOK {
				return errFake
			}
			f.states = []daemonUnit{{Name: "x.service", Scope: "user", Active: "active", Sub: "running"}}
			return nil
		},
		answers: func() bool {
			f.dials++
			return f.answer > 0 && f.dials >= f.answer
		},
		say:     say,
		dial:    time.Microsecond,
		recheck: 0,
		notice:  time.Hour,
		limit:   5 * time.Second,
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errFake = fakeErr("start refused")

func TestUnitWatchWaitsOutARestartWithoutStartingAnything(t *testing.T) {
	restarting := daemonUnit{Name: "x.service", Scope: "user", Active: "activating", Sub: "auto-restart"}
	f := &fakeUnit{states: []daemonUnit{restarting, restarting, {Name: "x.service", Scope: "user", Active: "active", Sub: "running"}}, answer: 5}
	var say bytes.Buffer
	if err := f.watch(&say).await(&restarting); err != nil {
		t.Fatal(err)
	}
	if f.starts != 0 {
		t.Fatalf("started the unit %d times during its own restart", f.starts)
	}
}

func TestUnitWatchStartsADownUserUnitOnce(t *testing.T) {
	down := daemonUnit{Name: "x.service", Scope: "user", Active: "inactive", Sub: "dead"}
	f := &fakeUnit{states: []daemonUnit{down}, answer: 3, startOK: true}
	var say bytes.Buffer
	w := f.watch(&say)
	w.recheck = time.Hour
	if err := w.await(&down); err != nil {
		t.Fatal(err)
	}
	if f.starts != 1 || !strings.Contains(say.String(), "starting it") {
		t.Fatalf("starts=%d say=%q", f.starts, say.String())
	}
}

func TestUnitWatchNamesTheJournalWhenTheStartedUnitDies(t *testing.T) {
	down := daemonUnit{Name: "x.service", Scope: "user", Active: "failed", Sub: "failed"}
	f := &fakeUnit{states: []daemonUnit{down}, startOK: true}
	w := f.watch(&bytes.Buffer{})
	w.start = func(*daemonUnit) error { f.starts++; return nil }
	err := w.await(&down)
	if err == nil || !strings.Contains(err.Error(), "journalctl --user -u x.service") || f.starts != 1 {
		t.Fatalf("starts=%d err=%v", f.starts, err)
	}
}

func TestUnitWatchReportsARefusedStart(t *testing.T) {
	down := daemonUnit{Name: "x.service", Scope: "user", Active: "inactive"}
	f := &fakeUnit{states: []daemonUnit{down}}
	err := f.watch(&bytes.Buffer{}).await(&down)
	if err == nil || !strings.Contains(err.Error(), "start refused") {
		t.Fatalf("err=%v", err)
	}
}

func TestUnitWatchLeavesADownSystemUnitToTheOperator(t *testing.T) {
	down := daemonUnit{Name: "figaro.service", Scope: "system", Active: "inactive"}
	f := &fakeUnit{states: []daemonUnit{down}, startOK: true}
	err := f.watch(&bytes.Buffer{}).await(&down)
	if err == nil || !strings.Contains(err.Error(), "sudo systemctl start figaro.service") || f.starts != 0 {
		t.Fatalf("starts=%d err=%v", f.starts, err)
	}
}

func TestUnitWatchGivesUpAtTheLimit(t *testing.T) {
	up := daemonUnit{Name: "x.service", Scope: "user", Active: "active", Sub: "running"}
	f := &fakeUnit{states: []daemonUnit{up}}
	w := f.watch(&bytes.Buffer{})
	w.limit = 20 * time.Millisecond
	if err := w.await(&up); err == nil || !strings.Contains(err.Error(), "has not answered") {
		t.Fatalf("err=%v", err)
	}
}

func TestParseUnitShowCarriesSubStateAndEnvironment(t *testing.T) {
	u := parseUnitShow("Id=f.service\nLoadState=loaded\nActiveState=activating\nSubState=auto-restart\nEnvironment=FIGARO_RUNTIME_DIR=/tmp/r \"A=b c\" D=\\\"q\\\"\n")
	if u.Sub != "auto-restart" || !u.restarting() || u.down() {
		t.Fatalf("%+v", u)
	}
	want := []string{"FIGARO_RUNTIME_DIR=/tmp/r", "A=b c", `D="q"`}
	if strings.Join(u.Env, "|") != strings.Join(want, "|") {
		t.Fatalf("env %q, want %q", u.Env, want)
	}
}
