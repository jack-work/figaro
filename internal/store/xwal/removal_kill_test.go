package xwal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// The removal's kill points, in the order they run. K1 is before the plan is
// durable; every later one is after it.
var removalKillPoints = []struct {
	name, step string
	committed  bool
}{
	{"K1-before-plan", "plan", false},
	{"K2-plan-durable", "sync-root", true},
	{"K3-between-markers", "unlink-marker", true},
	{"K4-markers-gone", "markers", true},
	{"K5-between-channels", "unlink-channel", true},
	{"K6-dirs-gone-plan-left", "dirs", true},
}

const (
	killDirEnv    = "XWAL_KILL_DIR"
	killStepEnv   = "XWAL_KILL_STEP"
	killVictimEnv = "XWAL_KILL_VICTIM"
	killOpEnv     = "XWAL_KILL_OP"
)

// Not a test on its own: the subprocess half of the kill tests.
func TestRemovalKillHelper(t *testing.T) {
	dir := os.Getenv(killDirEnv)
	if dir == "" {
		t.Skip("subprocess only")
	}
	f, err := openTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	step := os.Getenv(killStepEnv)
	f.testRemovalStep = func(s string) {
		if s == step {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	victim := os.Getenv(killVictimEnv)
	switch os.Getenv(killOpEnv) {
	case "component":
		c, err := f.Component(victim)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.RemoveComponent(c, "0123456789abcdef")
		t.Fatalf("RemoveComponent returned (%v) without reaching step %s", err, step)
	default:
		err = f.Remove(victim, true)
		t.Fatalf("Remove returned (%v) without reaching step %s", err, step)
	}
}

type killFixture struct {
	dir                     string
	survivor, victim, child string
	victimNode, childNode   string
	survivorTail            []string
}

func newKillFixture(t *testing.T) killFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	k := killFixture{dir: dir}
	if k.survivor, err = f.SpawnUnderRoot(); err != nil {
		t.Fatal(err)
	}
	if k.victim, err = f.SpawnUnderRoot(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		for _, tr := range []string{k.survivor, k.victim} {
			if _, _, err := f.Append(tr, 0, []byte(fmt.Sprintf(`"%s-%d"`, tr, i)), nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if k.child, err = f.ForkTail(k.victim); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Append(k.child, 0, []byte(`"child"`), nil); err != nil {
		t.Fatal(err)
	}
	k.victimNode, k.childNode = f.head(k.victim), f.head(k.child)
	k.survivorTail = mainPayloads(t, f, k.survivor)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return k
}

func mainPayloads(t *testing.T, f *Trunks, trunk string) []string {
	t.Helper()
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	l, err := x.channel(x.main).Log()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := uint64(1); i <= l.LastIndex(); i++ {
		_, p, err := x.Read(x.main, i)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(p))
	}
	return out
}

func runKilled(t *testing.T, k killFixture, op, step, victim string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRemovalKillHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), killDirEnv+"="+k.dir, killStepEnv+"="+step, killVictimEnv+"="+victim, killOpEnv+"="+op)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("subprocess was not killed at %s: %v\n%s", step, err, out)
	}
}

func assertGoneAfterReopen(t *testing.T, f *Trunks, k killFixture) {
	t.Helper()
	for _, tr := range []string{k.victim, k.child} {
		if _, err := f.Head(tr); !errors.Is(err, ErrUnknownTrunk) {
			t.Fatalf("after reopen, Head(%s) = %v, want ErrUnknownTrunk", tr, err)
		}
	}
	for _, n := range []string{k.victimNode, k.childNode} {
		if dirs := nodeDirs(t, f, n); len(dirs) != 0 {
			t.Fatalf("after reopen, node %s left dirs %v", n, dirs)
		}
	}
	ents, _ := os.ReadDir(f.root)
	for _, e := range ents {
		if len(e.Name()) > len(removalPrefix) && e.Name()[:len(removalPrefix)] == removalPrefix {
			t.Fatalf("after reopen, plan %s is left", e.Name())
		}
	}
}

func assertWholeAfterReopen(t *testing.T, f *Trunks, k killFixture) {
	t.Helper()
	for _, tr := range []string{k.victim, k.child} {
		x, err := f.Head(tr)
		if err != nil {
			t.Fatalf("after reopen, an uncommitted removal lost %s: %v", tr, err)
		}
		x.Close()
	}
	if got := mainPayloads(t, f, k.victim); len(got) < 6 {
		t.Fatalf("after reopen, victim reads %v", got)
	}
}

func TestARemovalSurvivesAKillAtEveryStep(t *testing.T) {
	for _, kp := range removalKillPoints {
		t.Run(kp.name, func(t *testing.T) {
			k := newKillFixture(t)
			runKilled(t, k, "remove", kp.step, k.victim)
			f, err := openTrunks(k.dir, trunksCfg())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if kp.committed {
				assertGoneAfterReopen(t, f, k)
			} else {
				assertWholeAfterReopen(t, f, k)
			}
			if got := mainPayloads(t, f, k.survivor); !slices.Equal(got, k.survivorTail) {
				t.Fatalf("survivor reads %v after reopen, want %v", got, k.survivorTail)
			}
		})
	}
}

// K0 for a move: the source half is killed before its plan is written, which
// is the window between the destination's acknowledgement and the source
// plan. The source must still hold the component whole.
func TestAMoveKilledBeforeTheSourcePlanKeepsTheSourceWhole(t *testing.T) {
	k := newKillFixture(t)
	runKilled(t, k, "component", "plan", k.child)
	f, err := openTrunks(k.dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	assertWholeAfterReopen(t, f, k)
}

// The plan file, its dir and the store root that holds it are durable, in
// that order, before the first marker is unlinked.
func TestARemovalSyncsItsPlanBeforeUnlinkingAMarker(t *testing.T) {
	k := newKillFixture(t)
	f, err := openTrunks(k.dir, trunksCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var steps []string
	f.testRemovalStep = func(s string) { steps = append(steps, s) }
	if err := f.Remove(k.victim, true); err != nil {
		t.Fatal(err)
	}
	at := func(s string) int { return slices.Index(steps, s) }
	order := []string{"plan", "sync-plan-file", "sync-plan-dir", "sync-root", "unlink-marker", "markers", "logical", "unlink-channel", "dirs"}
	for i := 1; i < len(order); i++ {
		if at(order[i-1]) < 0 || at(order[i]) < 0 || at(order[i-1]) > at(order[i]) {
			t.Fatalf("steps %v: %s must come before %s", steps, order[i-1], order[i])
		}
	}
}
