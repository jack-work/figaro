package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #60: while a unit restarts its daemon, a client that found no socket spawned
// its own; the unit's ExecCondition then saw that daemon and left the unit
// inactive. A client must defer to a unit that serves its runtime dir.

func fakeEnvSources(client map[string]string, manager []string, files map[string]string) unitEnvSources {
	return unitEnvSources{
		manager: func(string) []string { return manager },
		readFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		getenv: func(k string) string { return client[k] },
	}
}

func TestUnitGovernsOnlyItsOwnRuntimeDir(t *testing.T) {
	client := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}
	files := map[string]string{
		"/etc/fig.env":   "# pinned\nFIGARO_RUNTIME_DIR=\"/tmp/iso/run\"\nTOKEN=never-read-back\n",
		"/etc/other.env": "FIGARO_RUNTIME_DIR=/tmp/other\n",
	}
	for _, tc := range []struct {
		name    string
		unit    *daemonUnit
		manager []string
		ours    string
		want    bool
	}{
		{"no unit", nil, nil, "/run/user/1000/figaro", false},
		{"user unit on the default dir", &daemonUnit{Scope: "user"}, nil, "/run/user/1000/figaro", true},
		{"user unit, client on an isolated dir", &daemonUnit{Scope: "user"}, nil, "/tmp/iso/run", false},
		{"unit pinned to the client's dir", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/iso/run/"}}, nil, "/tmp/iso/run", true},
		{"unit pinned elsewhere", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/other"}}, nil, "/run/user/1000/figaro", false},
		{"system unit with its own xdg dir", &daemonUnit{Scope: "system", Env: []string{"XDG_RUNTIME_DIR=/run/figaro-x"}}, nil, "/run/figaro-x/figaro", true},
		{"system unit naming no dir", &daemonUnit{Scope: "system"}, nil, "/run/user/1000/figaro", false},
		{"unit pinned by an EnvironmentFile", &daemonUnit{Scope: "user", EnvFiles: []string{"/etc/fig.env"}}, nil, "/tmp/iso/run", true},
		{"an EnvironmentFile overrides Environment", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/iso/run"}, EnvFiles: []string{"/etc/other.env"}}, nil, "/tmp/iso/run", false},
		{"a missing EnvironmentFile is skipped", &daemonUnit{Scope: "user", EnvFiles: []string{"/etc/gone.env"}}, nil, "/run/user/1000/figaro", true},
		{"unit pinned by the manager environment", &daemonUnit{Scope: "user"}, []string{"XDG_RUNTIME_DIR=/run/user/1000", "FIGARO_RUNTIME_DIR=/tmp/iso/run"}, "/tmp/iso/run", true},
		{"Environment overrides the manager", &daemonUnit{Scope: "user", Env: []string{"FIGARO_RUNTIME_DIR=/tmp/other"}}, []string{"FIGARO_RUNTIME_DIR=/tmp/iso/run"}, "/tmp/iso/run", false},
		{"UnsetEnvironment drops a manager pin", &daemonUnit{Scope: "user", Unset: []string{"FIGARO_RUNTIME_DIR"}}, []string{"XDG_RUNTIME_DIR=/run/user/1000", "FIGARO_RUNTIME_DIR=/tmp/iso/run"}, "/run/user/1000/figaro", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unitGoverns(tc.unit, tc.ours, fakeEnvSources(client, tc.manager, files)); got != tc.want {
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
		working:  func(int) bool { return false },
		mayStart: true,
		sock:     "/run/x/angelus.sock",
		say:      say,
		dial:     time.Microsecond,
		recheck:  0,
		notice:   time.Hour,
		stall:    time.Hour,
		limit:    5 * time.Second,
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errFake = fakeErr("start refused")

func TestUnitWatchWaitsOutARestartWithoutStartingAnything(t *testing.T) {
	for _, restarting := range []daemonUnit{
		{Name: "x.service", Scope: "user", Active: "activating", Sub: "auto-restart"},
		{Name: "x.service", Scope: "user", Active: "inactive", Sub: "dead-before-auto-restart"},
		{Name: "x.service", Scope: "user", Active: "failed", Sub: "failed-before-auto-restart"},
	} {
		t.Run(restarting.Sub, func(t *testing.T) {
			f := &fakeUnit{states: []daemonUnit{restarting, restarting, {Name: "x.service", Scope: "user", Active: "active", Sub: "running"}}, answer: 5, startOK: true}
			if err := f.watch(&bytes.Buffer{}).await(&restarting); err != nil {
				t.Fatal(err)
			}
			if f.starts != 0 {
				t.Fatalf("started the unit %d times during its own restart", f.starts)
			}
		})
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

func TestUnitWatchStartsNothingUnderNoSelfSpawn(t *testing.T) {
	down := daemonUnit{Name: "x.service", Scope: "user", Active: "inactive", Sub: "dead"}
	f := &fakeUnit{states: []daemonUnit{down}, answer: 3, startOK: true}
	w := f.watch(&bytes.Buffer{})
	w.mayStart = false
	err := w.await(&down)
	if err == nil || !strings.Contains(err.Error(), "FIGARO_NO_SELF_SPAWN") || !strings.Contains(err.Error(), "systemctl --user start x.service") || f.starts != 0 {
		t.Fatalf("starts=%d err=%v", f.starts, err)
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

// A unit whose daemon is up but never binds the socket used to hold every
// client for the ten-minute startup cap.
func TestUnitWatchGivesUpOnAPidThatNeverBinds(t *testing.T) {
	up := daemonUnit{Name: "x.service", Scope: "user", Active: "active", Sub: "running", MainPID: 4242}
	f := &fakeUnit{states: []daemonUnit{up}}
	w := f.watch(&bytes.Buffer{})
	w.stall = 20 * time.Millisecond
	err := w.await(&up)
	if err == nil || !strings.Contains(err.Error(), "pid 4242") || !strings.Contains(err.Error(), "journalctl --user -u x.service") {
		t.Fatalf("err=%v", err)
	}
}

func TestUnitWatchWaitsOnAPidThatHoldsTheStore(t *testing.T) {
	up := daemonUnit{Name: "x.service", Scope: "user", Active: "active", Sub: "running", MainPID: 4242}
	f := &fakeUnit{states: []daemonUnit{up}, answer: 2000}
	w := f.watch(&bytes.Buffer{})
	w.dial = 20 * time.Microsecond
	w.stall = time.Millisecond
	w.working = func(pid int) bool { return pid == 4242 }
	if err := w.await(&up); err != nil {
		t.Fatalf("gave up on a daemon at work: %v", err)
	}
}

func TestUnitWatchRestartsTheStallClockForANewPid(t *testing.T) {
	a := daemonUnit{Name: "x.service", Scope: "user", Active: "active", Sub: "running", MainPID: 1}
	b := a
	b.MainPID = 2
	f := &fakeUnit{states: []daemonUnit{a, a, a, a, a, b}, answer: 12}
	w := f.watch(&bytes.Buffer{})
	w.dial = 40 * time.Millisecond
	w.stall = 400 * time.Millisecond
	if err := w.await(&a); err != nil {
		t.Fatalf("the stall clock carried over to a new pid: %v", err)
	}
}

func TestParseUnitShowCarriesSubStateAndEnvironment(t *testing.T) {
	u := parseUnitShow("Id=f.service\nLoadState=loaded\nActiveState=activating\nSubState=auto-restart\n" +
		"NeedDaemonReload=yes\n" +
		"ExecStart={ path=/nix/store/x-figaro/bin/figaro ; argv[]=/nix/store/x-figaro/bin/figaro --angelus ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }\n" +
		"Environment=FIGARO_RUNTIME_DIR=/tmp/r \"A=b c\" D=\\\"q\\\"\n" +
		"EnvironmentFiles=/etc/a.env (ignore_errors=no)\nEnvironmentFiles=/etc/b env (ignore_errors=yes)\n" +
		"UnsetEnvironment=FIGARO_ARIA C\n")
	if u.Sub != "auto-restart" || !u.restarting() || u.down() {
		t.Fatalf("%+v", u)
	}
	want := []string{"FIGARO_RUNTIME_DIR=/tmp/r", "A=b c", `D="q"`}
	if strings.Join(u.Env, "|") != strings.Join(want, "|") {
		t.Fatalf("env %q, want %q", u.Env, want)
	}
	if strings.Join(u.EnvFiles, "|") != "/etc/a.env|/etc/b env" || strings.Join(u.Unset, "|") != "FIGARO_ARIA|C" {
		t.Fatalf("files %q unset %q", u.EnvFiles, u.Unset)
	}
	if u.ExecPath != "/nix/store/x-figaro/bin/figaro" || !u.NeedReload {
		t.Fatalf("exec %q reload %v", u.ExecPath, u.NeedReload)
	}
}

func TestUpgradeServesOnlyThroughAUnitRunningTheTarget(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "figaro-new")
	old := filepath.Join(dir, "figaro-old")
	for _, p := range []string{to, old} {
		if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stable := filepath.Join(dir, "figaro")
	if err := os.Symlink(to, stable); err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	for _, tc := range []struct {
		name   string
		unit   daemonUnit
		getenv func(string) string
		want   string
	}{
		{"ExecStart is the target", daemonUnit{Name: "f.service", Scope: "user", ExecPath: to}, none, ""},
		{"ExecStart is a link to the target", daemonUnit{Name: "f.service", Scope: "user", ExecPath: stable}, none, ""},
		{"ExecStart is another build", daemonUnit{Name: "f.service", Scope: "user", ExecPath: old}, none, "runs " + old + ", not " + to},
		{"a stale unit file is named", daemonUnit{Name: "f.service", Scope: "user", ExecPath: old, NeedReload: true}, none, "daemon-reload"},
		{"a system unit is the operator's", daemonUnit{Name: "f.service", Scope: "system", ExecPath: to}, none, "sudo systemctl restart f.service"},
		{"no self spawn starts nothing", daemonUnit{Name: "f.service", Scope: "user", ExecPath: to}, func(k string) string {
			if k == "FIGARO_NO_SELF_SPAWN" {
				return "1"
			}
			return ""
		}, "FIGARO_NO_SELF_SPAWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := unitMayServe(&tc.unit, to, tc.getenv)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err=%v, want it to say %q", err, tc.want)
			}
		})
	}
}

// S6 of the train-2 review: upgrade restarted the unit onto its old build
// and reported the new one running.
func TestUpgradeConfirmsTheServingBuild(t *testing.T) {
	dir := t.TempDir()
	to := filepath.Join(dir, "figaro-new")
	old := filepath.Join(dir, "figaro-old")
	for _, p := range []string{to, old} {
		if err := os.WriteFile(p, []byte(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	u := &daemonUnit{Name: "f.service", Scope: "user", MainPID: 10}
	exe := map[int]string{10: to, 11: to, 12: old}
	exeOf := func(pid int) string { return exe[pid] }
	if err := servingMismatch(u, 10, to, exeOf); err != nil {
		t.Fatalf("refused the target build: %v", err)
	}
	if err := servingMismatch(u, 11, to, exeOf); err == nil || !strings.Contains(err.Error(), "outside the unit") {
		t.Fatalf("a daemon outside the unit passed: %v", err)
	}
	u.MainPID = 12
	if err := servingMismatch(u, 12, to, exeOf); err == nil || !strings.Contains(err.Error(), "runs "+old) {
		t.Fatalf("the old build passed: %v", err)
	}
	if err := servingMismatch(u, 0, to, exeOf); err == nil {
		t.Fatal("an unknown socket owner passed")
	}
}
