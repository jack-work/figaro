//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/jack-work/figaro/internal/tape"
)

type lockstepRun struct {
	steps [][]byte
	dones []int64
	read  int64
}

// lockstepDrive runs `figaro replay --lockstep` the way the stage manager's
// driver does: its own pty, no controlling terminal, a socket it steps. With a
// pause it sleeps that long of wall time before every 25th step, so a frame
// that read the wall clock lands differently.
func lockstepDrive(t *testing.T, bin, tapePath string, steps int, env []string, pause time.Duration) lockstepRun {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{"run", "state", "cache", "config"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(root, "ls.sock")
	var base []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "HUSH_") || k == "TMUX" || k == "GOMAXPROCS" {
			continue
		}
		base = append(base, kv)
	}
	base = append(base,
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-lockstep",
		"FIGARO_NO_SELF_SPAWN=1",
		"BROWSER=true",
		"TERM=xterm-256color",
	)
	cmd := exec.Command(bin, "replay", "--lockstep", sock, tapePath)
	cmd.Env = append(base, env...)
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, &stderr
	// A test binary that dies on -timeout or a signal runs no defer and no
	// cleanup; without this the replay outlives it, reparented to init.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Errorf("the replay did not exit after the driver left; stderr:\n%s", stderr.String())
		}
	}()

	var conn net.Conn
	for deadline := time.Now().Add(15 * time.Second); ; {
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the replay never listened on its socket; stderr:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	screen := make(chan []byte, 64)
	go func() {
		defer close(screen)
		buf := make([]byte, 64<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				screen <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	answers := bufio.NewReader(conn)
	var run lockstepRun
	var pending []byte
	for n := 0; n < steps; n++ {
		if pause > 0 && n%25 == 0 {
			time.Sleep(pause)
		}
		if _, err := fmt.Fprintf(conn, "step %d\n", n); err != nil {
			t.Fatal(err)
		}
		line, err := answers.ReadString('\n')
		if err != nil {
			t.Fatalf("step %d: the socket said nothing (%v); stderr:\n%s", n, err, stderr.String())
		}
		var got, total int64
		if _, err := fmt.Sscanf(line, "done %d %d", &got, &total); err != nil || got != int64(n) {
			t.Fatalf("step %d answered %q", n, line)
		}
		run.dones = append(run.dones, total)
		marker := stepMarker(uint64(n))
		for !bytes.Contains(pending, marker) {
			chunk, ok := <-screen
			if !ok {
				t.Fatalf("step %d: the pty closed before its marker; stderr:\n%s", n, stderr.String())
			}
			pending = append(pending, chunk...)
		}
		i := bytes.Index(pending, marker)
		run.steps = append(run.steps, append([]byte(nil), pending[:i]...))
		run.read += int64(i + len(marker))
		pending = pending[i+len(marker):]
		if total != run.read {
			t.Fatalf("step %d: done counts %d bytes, the pty gave %d", n, total, run.read)
		}
		if bytes.Contains(run.steps[n], []byte("\x1b]7717;")) {
			t.Fatalf("step %d carries a marker that is not its own", n)
		}
	}
	conn.Close()
	return run
}

func tapeSteps(t *testing.T, path string, fps int) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	last := 0.0
	for sc.Scan() {
		var fr struct {
			T *float64 `json:"t"`
		}
		if json.Unmarshal(sc.Bytes(), &fr) == nil && fr.T != nil {
			last = math.Max(last, *fr.T)
		}
	}
	return int((last+1)*float64(fps)) + 1
}

// A lockstep replay is a pure function of the tape and the step: two runs
// give the same bytes on the same steps, whatever the scheduler does and
// however long the driver dawdles between steps. One wall-clock read on the
// render path breaks this (sabotage canary: put `t.now = time.Now` back in
// transcript_render.go and the paused run diverges).
func TestALockstepReplayIsThePureFunctionOfItsSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	bin := lockstepBinary(t)
	for _, name := range []string{"spike.tape", "stream-eager.tape", "stream-buffered.tape"} {
		t.Run(name, func(t *testing.T) {
			path, err := filepath.Abs(filepath.Join("testdata", "tapes", name))
			if err != nil {
				t.Fatal(err)
			}
			steps := tapeSteps(t, path, lockstepFPS)
			a := lockstepDrive(t, bin, path, steps, []string{"GOMAXPROCS=1"}, 0)
			b := lockstepDrive(t, bin, path, steps, []string{"GOMAXPROCS=8"}, 70*time.Millisecond)
			drawn := 0
			for n := range a.steps {
				if len(a.steps[n]) > 0 {
					drawn++
				}
				if !bytes.Equal(a.steps[n], b.steps[n]) {
					t.Fatalf("step %d differs between runs:\n  %q\n  %q", n, clip(a.steps[n]), clip(b.steps[n]))
				}
			}
			if drawn < 2 {
				t.Fatalf("only %d of %d steps drew anything: the tape never reached the screen", drawn, steps)
			}
		})
	}
}

// The spinner turns on the animation clock, on whole steps of the frame grid:
// at the default 12 fps a step draws only when a tape frame falls due on it or
// when it is a multiple of 5.
func TestALockstepReplayDrawsOnlyOnFramesAndTheAnimationGrid(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the binary in a pty; skipped under -short")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "tapes", "stream-eager.tape"))
	if err != nil {
		t.Fatal(err)
	}
	due := map[int]bool{}
	_, frames, err := tape.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if f.Dir == tape.In && f.Method() != "" {
			due[int(math.Ceil(f.T*lockstepFPS-1e-9))] = true
		}
	}
	steps := tapeSteps(t, path, lockstepFPS)
	run := lockstepDrive(t, lockstepBinary(t), path, steps, nil, 0)
	ticks := 0
	for n := 1; n < steps; n++ {
		if len(run.steps[n]) == 0 {
			continue
		}
		switch {
		case due[n]:
		case n%5 == 0:
			ticks++
		default:
			t.Fatalf("step %d drew with no frame due and off the 12 fps grid: %q", n, clip(run.steps[n]))
		}
	}
	if ticks < 60 {
		t.Fatalf("only %d animation ticks drew over a streaming turn", ticks)
	}
}

func TestALockstepReplayWithNoDriverExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the driver timeout; skipped under -short")
	}
	path, err := filepath.Abs(filepath.Join("testdata", "tapes", "spike.tape"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cmd := exec.Command(lockstepBinary(t), "replay", "--lockstep", filepath.Join(root, "ls.sock"), path)
	cmd.Env = append(os.Environ(), "FIGARO_RUNTIME_DIR="+root, "FIGARO_STATE_DIR="+root,
		"FIGARO_CONFIG_DIR="+root, "FIGARO_CACHE_DIR="+root, "FIGARO_NO_SELF_SPAWN=1", "BROWSER=true")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a lockstep replay with no driver exited 0:\n%s", out)
	}
	if took := time.Since(start); took > lockstepWait+5*time.Second {
		t.Fatalf("it took %s to give up on the driver", took)
	}
	if !strings.Contains(string(out), "no driver connected") {
		t.Fatalf("no reason given:\n%s", out)
	}
}

func lockstepBinary(t *testing.T) string {
	t.Helper()
	if !raceEnabled {
		return smokeBinary(t)
	}
	bin := filepath.Join(t.TempDir(), "figaro-race")
	build := exec.Command("go", "build", "-race", "-o", bin, "./cmd/figaro")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -race: %v\n%s", err, out)
	}
	return bin
}

func clip(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…(" + strconv.Itoa(len(b)) + " bytes)"
	}
	return string(b)
}
