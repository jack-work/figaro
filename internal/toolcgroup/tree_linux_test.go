//go:build linux

package toolcgroup

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeUnit lays out a delegated unit cgroup in a temp dir: the files a
// Delegate=yes unit hands its main process, with the given controllers.
func fakeUnit(t *testing.T, controllers string) (mount, unitDir string) {
	t.Helper()
	mount = t.TempDir()
	unitDir = filepath.Join(mount, "u")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"cgroup.procs": "111\n222\n", "cgroup.controllers": controllers, "cgroup.subtree_control": ""} {
		if err := os.WriteFile(filepath.Join(unitDir, f), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return mount, unitDir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists (want absent): %v", path, err)
	}
}

// kernelLike gives a tree the cgroupfs semantics a plain dir lacks: a move takes
// the pid out of whichever cgroup holds it (a pid in stuck refuses to move, as a
// task mid-fork or exiting may), and rmdir of a cgroup that still holds a
// process fails with EBUSY.
func kernelLike(t *testing.T, tree *Tree, mount string, stuck ...string) *Tree {
	t.Helper()
	tree.move = func(pid, toDir string) error {
		if slices.Contains(stuck, pid) {
			return unix.EBUSY
		}
		_ = filepath.WalkDir(mount, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "cgroup.procs" {
				return nil
			}
			if pids := strings.Fields(read(t, p)); slices.Contains(pids, pid) {
				os.WriteFile(p, []byte(strings.Join(slices.DeleteFunc(pids, func(x string) bool { return x == pid }), "\n")), 0o644)
			}
			return nil
		})
		dst := filepath.Join(toDir, "cgroup.procs")
		cur, _ := os.ReadFile(dst)
		return os.WriteFile(dst, []byte(strings.TrimSpace(string(cur)+"\n"+pid)), 0o644)
	}
	tree.rmdir = func(dir string) error {
		if len(procsOf(dir)) > 0 {
			return unix.EBUSY
		}
		return os.RemoveAll(dir)
	}
	return tree
}

var sample = Limits{ParentCPUMax: "700000 100000", ParentMemoryHigh: "24G", ToolCPUMax: "150000 100000", ToolCPUWeight: 100, DaemonCPUWeight: 1000}

func TestAParentedTreeMovesTheDaemonOutAndBuildsTheToolsParent(t *testing.T) {
	mount, u := fakeUnit(t, "cpu io memory pids")
	root, err := kernelLike(t, At(mount, "/u", NewRegistry(), sample), mount).Root()
	if err != nil || root != "/u/tools" {
		t.Fatalf("root %q, err %v; want /u/tools", root, err)
	}
	if got := strings.Fields(read(t, filepath.Join(u, "daemon", "cgroup.procs"))); !slices.Equal(got, []string{"111", "222", strconv.Itoa(os.Getpid())}) {
		t.Errorf("daemon/cgroup.procs = %v, want the unit's processes and this daemon", got)
	}
	for path, want := range map[string]string{
		"cgroup.procs":                 "",
		"cgroup.subtree_control":       "+cpu +memory",
		"daemon/cpu.weight":            "1000",
		"tools/cpu.max":                "700000 100000",
		"tools/memory.high":            "24G",
		"tools/cgroup.subtree_control": "+cpu",
	} {
		if got := read(t, filepath.Join(u, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	absent(t, filepath.Join(u, "daemon", "cpu.max")) // the daemon stays uncapped
}

func TestEveryToolCgroupIsBornUnderTheParentWithItsOwnLimits(t *testing.T) {
	mount, u := fakeUnit(t, "cpu memory")
	reg := NewRegistry()
	tree := At(mount, "/u", reg, sample)
	fd, err := tree.FD("abc123")
	if err != nil || fd < 0 {
		t.Fatalf("fd %d, err %v", fd, err)
	}
	if got := read(t, filepath.Join(u, "tools", "abc123", "cpu.max")); got != "150000 100000" {
		t.Errorf("tool cpu.max = %q", got)
	}
	if got := read(t, filepath.Join(u, "tools", "abc123", "cpu.weight")); got != "100" {
		t.Errorf("tool cpu.weight = %q", got)
	}
	absent(t, filepath.Join(u, "tools-abc123"))
	if f, ok := reg.Lookup("/u/tools/abc123"); !ok || f != "abc123" {
		t.Errorf("Lookup = %q %v, want abc123 true", f, ok)
	}
}

func TestWithoutDelegatedMemoryOnlyCPUIsArranged(t *testing.T) {
	mount, u := fakeUnit(t, "cpu io pids")
	if _, err := At(mount, "/u", NewRegistry(), sample).Root(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(u, "cgroup.subtree_control")); got != "+cpu" {
		t.Errorf("subtree_control = %q, want +cpu", got)
	}
	absent(t, filepath.Join(u, "tools", "memory.high"))
	if got := read(t, filepath.Join(u, "tools", "cpu.max")); got != "700000 100000" {
		t.Errorf("tools cpu.max = %q", got)
	}
}

func TestNoLimitsKeepsTheFlatLayout(t *testing.T) {
	mount, u := fakeUnit(t, "cpu memory")
	if _, err := At(mount, "/u", NewRegistry(), Limits{}).FD("abc123"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(u, "tools-abc123")); err != nil {
		t.Fatalf("flat tool cgroup missing: %v", err)
	}
	absent(t, filepath.Join(u, "daemon"))
	absent(t, filepath.Join(u, "tools"))
	if got := read(t, filepath.Join(u, "cgroup.subtree_control")); got != "" {
		t.Errorf("subtree_control = %q, want untouched", got)
	}
}

func TestADaemonAlreadyInItsLeafArrangesItsUnit(t *testing.T) {
	mount, u := fakeUnit(t, "cpu")
	if err := os.Mkdir(filepath.Join(u, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := At(mount, "/u/daemon", NewRegistry(), sample).Root()
	if err != nil || root != "/u/tools" {
		t.Fatalf("root %q, err %v; want /u/tools", root, err)
	}
	absent(t, filepath.Join(u, "daemon", "tools"))
}

func TestAUnitThatKeepsAProcessGetsNoLimitsButToolsStillRun(t *testing.T) {
	mount, u := fakeUnit(t, "cpu")
	// subtree_control as a directory: every write refused, as the kernel refuses
	// +cpu while a process stays in the unit.
	if err := os.Remove(filepath.Join(u, "cgroup.subtree_control")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(u, "cgroup.subtree_control"), 0o755); err != nil {
		t.Fatal(err)
	}
	tree := At(mount, "/u", NewRegistry(), sample)
	if _, err := tree.FD("abc123"); err != nil {
		t.Fatalf("tools must still run (attributed, unlimited): %v", err)
	}
	absent(t, filepath.Join(u, "tools", "cpu.max"))
	absent(t, filepath.Join(u, "tools", "abc123", "cpu.max"))
}

func TestAnUndelegatedUnitRefusesNamingDelegate(t *testing.T) {
	mount, u := fakeUnit(t, "cpu")
	if err := os.Remove(filepath.Join(u, "cgroup.procs")); err != nil {
		t.Fatal(err)
	}
	_, err := At(mount, "/u", NewRegistry(), sample).Root()
	if err == nil || !strings.Contains(err.Error(), "Delegate=yes") {
		t.Fatalf("err = %v, want one naming Delegate=yes", err)
	}
	absent(t, filepath.Join(u, "daemon"))
}

func legacyLeaf(t *testing.T, u, name, procs string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(u, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(u, name, "cgroup.procs"), []byte(procs), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAnEarlierDaemonsToolCgroupsAreAdoptedUnderTheParent(t *testing.T) {
	mount, u := fakeUnit(t, "cpu memory")
	legacyLeaf(t, u, "tools-abc", "333\n444\n")
	legacyLeaf(t, u, "tools-def", "")
	reg := NewRegistry()
	if _, err := kernelLike(t, At(mount, "/u", reg, sample), mount).Root(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(read(t, filepath.Join(u, "tools", "abc", "cgroup.procs"))); !slices.Equal(got, []string{"333", "444"}) {
		t.Errorf("adopted cgroup.procs = %v, want [333 444]", got)
	}
	if got := read(t, filepath.Join(u, "tools", "abc", "cpu.max")); got != "150000 100000" {
		t.Errorf("adopted cpu.max = %q", got)
	}
	absent(t, filepath.Join(u, "tools-abc"))
	absent(t, filepath.Join(u, "tools-def"))
	absent(t, filepath.Join(u, "tools", "def")) // an empty one is only removed
	if f, ok := reg.Lookup("/u/tools/abc"); !ok || f != "abc" {
		t.Errorf("Lookup(adopted) = %q %v, want abc true", f, ok)
	}
}

// A shell that does not move keeps its old leaf populated: rmdir says EBUSY.
// The leaf stays attributed to its aria (never the owner) and capped as one
// tool cgroup.
func TestALeftoverPopulatedLeafIsRegisteredAndCappedAndNeverTheOwner(t *testing.T) {
	mount, u := fakeUnit(t, "cpu")
	legacyLeaf(t, u, "tools-abc", "333\n555\n")
	reg := NewRegistry()
	tree := kernelLike(t, At(mount, "/u", reg, sample), mount, "555")
	if _, err := tree.FD("xyz"); err != nil {
		t.Fatalf("tools must still run: %v", err)
	}
	if got := strings.Fields(read(t, filepath.Join(u, "tools-abc", "cgroup.procs"))); !slices.Equal(got, []string{"555"}) {
		t.Fatalf("leftover leaf procs = %v, want [555] (EBUSY surfaced, not hidden)", got)
	}
	if f, ok := reg.Lookup("/u/tools-abc"); !ok || f != "abc" {
		t.Errorf("leftover leaf: Lookup = %q %v, want abc true (attributed, never the owner)", f, ok)
	}
	if got := read(t, filepath.Join(u, "tools-abc", "cpu.max")); got != "150000 100000" {
		t.Errorf("leftover leaf cpu.max = %q, want the per-tool cap", got)
	}
	if reg.Suspect("/u/tools-zzz", 555) == "" {
		t.Error("an unregistered leaf in the unit was not suspected")
	}
}

func TestTheParentedLayoutGuardsTheWholeUnit(t *testing.T) {
	mount, _ := fakeUnit(t, "cpu")
	reg := NewRegistry()
	if _, err := kernelLike(t, At(mount, "/u", reg, sample), mount).Root(); err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	for _, c := range []struct {
		cg      string
		pid     int
		suspect bool
	}{
		{"/u/tools/zzz", 7, true}, {"/u/tools", 7, true}, {"/u/tools/zzz/sub", 7, true},
		{"/u/daemon", 7, true}, {"/u", 7, true}, {"/u/tools-old", 7, true}, {"/u/anything", 7, true},
		{"/u/daemon", me, false}, {"/user.slice/session-1", 7, false}, {"/ux", 7, false},
	} {
		if got := reg.Suspect(c.cg, c.pid) != ""; got != c.suspect {
			t.Errorf("Suspect(%q, %d) = %v, want %v", c.cg, c.pid, got, c.suspect)
		}
	}
	if NewRegistry().Suspect("/u/tools/zzz", 7) != "" {
		t.Error("a flat (unguarded) registry suspected a caller")
	}
}

func TestLimitsCheck(t *testing.T) {
	ok := []Limits{{}, sample, {ParentCPUMax: "max", ParentMemoryHigh: "max"}, {ParentCPUMax: "700000"}, {ParentMemoryHigh: "25769803776"},
		{ParentCPUMax: "max 100000"}, {ToolCPUMax: "1000 1000"}, {ToolCPUMax: "1000 1000000"}}
	for _, l := range ok {
		if err := l.Check(); err != nil {
			t.Errorf("%+v refused: %v", l, err)
		}
	}
	bad := map[string]Limits{
		"parent_cpu_max":     {ParentCPUMax: "7 CPUs"},
		"parent_cpu_max ":    {ParentCPUMax: "700000 1"},     // period under 1000: the kernel says EINVAL
		"tool_cpu_max ":      {ToolCPUMax: "999 100000"},     // quota under 1000
		"tool_cpu_max  ":     {ToolCPUMax: "150000 1000001"}, // period over 1000000
		"tool_cpu_max":       {ToolCPUMax: "0 100000"},
		"parent_memory_high": {ParentMemoryHigh: "24 GiB"},
		"tool_cpu_weight":    {ToolCPUWeight: 10001},
		"daemon_cpu_weight":  {DaemonCPUWeight: -1},
	}
	for key, l := range bad {
		if err := l.Check(); err == nil || !strings.Contains(err.Error(), strings.TrimSpace(key)) {
			t.Errorf("%+v: err %v, want one naming %s", l, err, key)
		}
	}
	if (Limits{}).Parented() || !sample.Parented() {
		t.Error("Parented: zero must be flat, sample parented")
	}
}
