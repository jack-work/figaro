//go:build linux

package valet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/box"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if os.Getenv("VALET_LANDLOCK_HELPER") == "1" {
		runLandlockHelper()
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "valet" {
		var err error
		switch os.Args[2] {
		case "fence":
			err = RunFence()
		case "fenced":
			err = RunFenced(context.Background())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type world struct {
	dir, state, granted, readonly, secret, hidden, door string
	v                                                   Valet
}

func newWorld(t *testing.T) world {
	t.Helper()
	if err := Available(); err != nil {
		t.Skip(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := world{
		dir:      dir,
		state:    filepath.Join(dir, "home", ".local", "state", "figaro-valet"),
		granted:  filepath.Join(dir, "home", "dev", "tree"),
		readonly: filepath.Join(dir, "home", "dev", "docs"),
		secret:   filepath.Join(dir, "home", "dev", "private"),
		hidden:   filepath.Join(dir, "home", "dev", "conf"),
		door:     filepath.Join(dir, "run", "figaro", "angelus.sock"),
	}
	for _, d := range []string{w.state, w.granted, w.hidden, w.readonly, w.secret, filepath.Dir(w.door)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(w.granted, "file"):  "granted\n",
		filepath.Join(w.hidden, "key"):    "hidden\n",
		filepath.Join(w.readonly, "file"): "readonly\n",
		filepath.Join(w.secret, "file"):   "secret\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("unix", w.door)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	tab := Table{}
	tab.Add(Grant{Figaro: Anyone, Path: w.granted, Mode: "rw", Granted: time.Now(), Expires: time.Now().Add(time.Hour)})
	tab.Add(Grant{Figaro: "3006ff4b42ca", Path: w.readonly, Mode: "ro", Granted: time.Now(), Expires: time.Now().Add(time.Hour)})
	tab.Add(Grant{Figaro: Anyone, Path: w.hidden, Mode: "rw", Granted: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err := tab.Save(w.state); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w.v = Valet{
		Peers:    []int{os.Getuid()},
		State:    w.state,
		MaxGrant: DefaultMaxGrant,
		Home:     filepath.Join(dir, "home"),
		Hide:     []string{w.hidden},
		Self:     self,
		Log:      os.Stderr,
	}
	return w
}

func (w world) open(t *testing.T, figaro, workspace string) (sandbox.Sandbox, *exec.Cmd, error) {
	t.Helper()
	sock := filepath.Join(w.dir, "exec.sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type answered struct {
		cmd *exec.Cmd
		err error
	}
	got := make(chan answered, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			got <- answered{err: err}
			return
		}
		defer nc.Close()
		cmd, err := w.v.Answer(nc.(*net.UnixConn), time.Now())
		got <- answered{cmd, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, openErr := box.Provider{Name: "valet", Socket: sock}.Open(ctx, sandbox.Spec{Figaro: figaro, Workspace: workspace})
	a := <-got
	if a.cmd != nil {
		t.Cleanup(func() { a.cmd.Wait() })
	}
	if openErr == nil {
		t.Cleanup(func() { b.Close() })
	}
	if openErr != nil {
		return nil, a.cmd, openErr
	}
	return b, a.cmd, a.err
}

func run(t *testing.T, b sandbox.Sandbox, command string) string {
	t.Helper()
	var out strings.Builder
	p, err := b.Exec(context.Background(), sandbox.ExecRequest{Command: command, OnChunk: func(c []byte) { out.Write(c) }})
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	return out.String()
}

func TestAToolSeesOnlyWhatItsFigaroWasGranted(t *testing.T) {
	w := newWorld(t)
	b, _, err := w.open(t, "3006ff4b42cafe", w.granted)
	if err != nil {
		t.Fatal(err)
	}
	probe := strings.Join([]string{
		"pwd",
		"cat " + filepath.Join(w.granted, "file"),
		"echo wrote > " + filepath.Join(w.granted, "new") + " && echo granted-writable",
		"cat " + filepath.Join(w.readonly, "file"),
		"{ echo x > " + filepath.Join(w.readonly, "new") + "; } 2>/dev/null || echo readonly-refused",
		"test -e " + filepath.Join(w.secret, "file") + " && echo SECRET-VISIBLE || echo secret-absent",
		"test -e " + GrantsPath(w.state) + " && echo TABLE-VISIBLE || echo table-absent",
		"test -e " + filepath.Join(w.hidden, "key") + " && echo HIDDEN-VISIBLE || echo hidden-absent",
		"test -S " + w.door + " && echo DAEMON-VISIBLE || echo daemon-absent",
		"test -e /run/user && echo RUNTIME-VISIBLE || echo runtime-absent",
		"ls /proc | grep -c '^[0-9]'",
		"grep CapEff /proc/self/status",
		"grep NoNewPrivs /proc/self/status",
		"id -u",
	}, "; ")
	got := strings.Split(strings.TrimSpace(run(t, b, probe)), "\n")
	want := []string{
		w.granted, "granted", "granted-writable", "readonly", "readonly-refused", "secret-absent",
		"table-absent", "hidden-absent", "daemon-absent", "runtime-absent",
	}
	if len(got) < len(want)+4 {
		t.Fatalf("probe printed %q", got)
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("line %d: %q, want %q", i, got[i], line)
		}
	}
	if n := got[len(want)]; n == "0" || len(n) > 1 {
		t.Errorf("the fence's /proc lists %s processes: it is not its own pid namespace", n)
	}
	if capEff := got[len(want)+1]; !strings.HasSuffix(capEff, "0000000000000000") {
		t.Errorf("a tool holds capabilities: %s", capEff)
	}
	if nnp := got[len(want)+2]; !strings.HasSuffix(nnp, "1") {
		t.Errorf("no_new_privs is not set: %s", nnp)
	}
	if uid := got[len(want)+3]; uid != fmt.Sprint(os.Getuid()) {
		t.Errorf("the tool runs as %s, not as the person (%d)", uid, os.Getuid())
	}
	if data, err := os.ReadFile(filepath.Join(w.granted, "new")); err != nil || string(data) != "wrote\n" {
		t.Errorf("a write through the grant did not land: %q %v", data, err)
	}
	if data, err := b.FS().ReadFile(filepath.Join(w.secret, "file")); err == nil {
		t.Errorf("box.fs read an ungranted file: %q", data)
	}
}

func TestAFigaroWithoutGrantsGetsItsScratch(t *testing.T) {
	w := newWorld(t)
	b, _, err := w.open(t, "44eb638841e9", w.readonly)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(run(t, b, "pwd; test -e "+filepath.Join(w.readonly, "file")+" && echo RO-VISIBLE || echo ro-absent"))
	if len(got) != 2 || got[0] != filepath.Join(w.state, "scratch", "44eb638841e9") || got[1] != "ro-absent" {
		t.Fatalf("probe printed %q", got)
	}
}

func TestTheValetRefusesWhatItMustNotServe(t *testing.T) {
	w := newWorld(t)
	stranger := w
	stranger.v.Peers = []int{os.Getuid() + 1}
	if _, _, err := stranger.open(t, "3006ff4b42cafe", ""); err == nil || !strings.Contains(err.Error(), "may not call this valet") {
		t.Errorf("a stranger: %v", err)
	}
	if _, _, err := w.open(t, "../escape", ""); err == nil || !strings.Contains(err.Error(), "does not name a figaro") {
		t.Errorf("a path as a figaro: %v", err)
	}
}

func TestTheFenceDiesWithItsConnection(t *testing.T) {
	w := newWorld(t)
	b, cmd, err := w.open(t, "3006ff4b42cafe", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sessions().Start(sandbox.ExecRequest{Command: "sleep 300"}); err != nil {
		t.Fatal(err)
	}
	b.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Signal(syscall.SIGKILL)
		t.Fatal("the fence outlived its connection")
	}
}

func runLandlockHelper() {
	absSock := os.Getenv("VALET_LL_ABS")
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		fmt.Fprintln(os.Stderr, "private /:", err)
		os.Exit(2)
	}
	if err := unix.Mount("tmpfs", "/run", "tmpfs", 0, "mode=0700"); err != nil {
		fmt.Fprintln(os.Stderr, "tmpfs /run:", err)
		os.Exit(2)
	}
	roDir, rwDir := "/run/ro", "/run/rw"
	for _, d := range []string{roDir, rwDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "mkdir:", err)
			os.Exit(2)
		}
	}
	f := Fence{
		Scratch: rwDir,
		Grants:  []Mount{{Path: roDir, Write: false}},
	}
	if err := f.Lock(); err != nil {
		fmt.Fprintln(os.Stderr, "lock:", err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(rwDir, "ok"), []byte("ok"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "rw write failed:", err)
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(roDir, "denied"), []byte("bad"), 0o600); err == nil {
		fmt.Fprintln(os.Stderr, "landlock allowed write into read-only grant")
		os.Exit(2)
	}
	errs := make(chan error, 16)
	for range 16 {
		go func() {
			if err := exec.Command("sh", "-c", "echo bad > /run/ro/from-child").Run(); err == nil {
				errs <- errors.New("child thread bypassed Landlock")
				return
			}
			errs <- nil
		}()
	}
	for range 16 {
		if err := <-errs; err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if absSock != "" && LandlockABI() >= 6 {
		if c, err := net.Dial("unix", "\x00"+absSock); err == nil {
			c.Close()
			fmt.Fprintln(os.Stderr, "landlock allowed abstract unix socket connect across domains")
			os.Exit(2)
		}
	}
	fmt.Println("landlock-ok")
	os.Exit(0)
}

func TestLandlockEnforcesReadOnlyAndAbstractSocketScope(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	if LandlockABI() == 0 {
		t.Skip("kernel has Landlock disabled")
	}
	absName := fmt.Sprintf("valet-ll-%d-%d", os.Getpid(), time.Now().UnixNano())
	ln, err := net.Listen("unix", "\x00"+absName)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()

	uid, gid := os.Getuid(), os.Getgid()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"VALET_LANDLOCK_HELPER=1",
		"VALET_LL_ABS="+absName,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
		AmbientCaps:                []uintptr{unix.CAP_SYS_ADMIN},
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "landlock-ok") {
		t.Fatalf("landlock helper failed: %v\n%s", err, out)
	}
}

// A tool holding rw on a tree can replace the parent of a nested grant with a
// symlink; the next fence must not follow it and mount what it points at.
func TestASymlinkPlantedOverANestedGrantRedirectsNothing(t *testing.T) {
	w := newWorld(t)
	inner := filepath.Join(w.granted, "a", "b")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "file"), []byte("inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tab, err := Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	tab.Add(Grant{Figaro: "3006ff4b42ca", Path: inner, Mode: "rw", Granted: time.Now(), Expires: time.Now().Add(time.Hour)})
	if err := tab.Save(w.state); err != nil {
		t.Fatal(err)
	}
	first, _, err := w.open(t, "3006ff4b42cafe", w.granted)
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, first, "cat a/b/file; mv a a2 && mkdir a && ln -s ../.. a/b && echo planted"); !strings.Contains(got, "inner") || !strings.Contains(got, "planted") {
		t.Fatalf("the first call could not plant the swap: %q", got)
	}
	first.Close()
	second, _, err := w.open(t, "3006ff4b42cafe", w.granted)
	if err != nil {
		t.Fatal(err)
	}
	got := run(t, second, "test -e "+filepath.Join(w.secret, "file")+" && echo SECRET-VISIBLE || echo secret-absent; "+
		"{ echo pwned > "+filepath.Join(w.secret, "planted")+"; } 2>/dev/null && echo SECRET-WRITABLE || echo secret-unwritable")
	if strings.TrimSpace(got) != "secret-absent\nsecret-unwritable" {
		t.Errorf("the second call reached an ungranted tree: %q", got)
	}
	if _, err := os.Stat(filepath.Join(w.secret, "planted")); err == nil {
		t.Error("a write landed in an ungranted tree on the host")
	}
}

// A hide below a grant could be uncovered by renaming its parent between
// calls, so a grant that holds a hide is dropped instead of shadowed.
func TestAGrantHoldingAHideIsDropped(t *testing.T) {
	w := newWorld(t)
	vault := filepath.Join(w.granted, "deep", "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "key"), []byte("vault\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.v.Hide = append(w.v.Hide, vault)
	b, _, err := w.open(t, "3006ff4b42cafe", "")
	if err != nil {
		t.Fatal(err)
	}
	got := run(t, b, "test -e "+filepath.Join(w.granted, "file")+" && echo TREE-VISIBLE || echo tree-absent; "+
		"test -e "+filepath.Join(w.readonly, "file")+" && echo ro-visible || echo RO-ABSENT")
	if strings.TrimSpace(got) != "tree-absent\nro-visible" {
		t.Errorf("probe printed %q", got)
	}
}

// A caller that is not a peer is judged before its opening is read, and
// one that holds the connection open without speaking is let go.
func TestAStrangerIsJudgedBeforeItSpeaksAndNotWaitedOn(t *testing.T) {
	w := newWorld(t)
	w.v.Peers = []int{os.Getuid() + 1}
	sock := filepath.Join(w.dir, "silent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	silent, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	nc, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	start := time.Now()
	_, err = w.v.Answer(nc.(*net.UnixConn), time.Now())
	if err == nil || !strings.Contains(err.Error(), "may not call this valet") {
		t.Errorf("a silent stranger: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the valet waited %s on a silent stranger", took)
	}
}
