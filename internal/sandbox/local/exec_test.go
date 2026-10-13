package local

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/jack-work/figaro/api/sandbox"
)

func open(t *testing.T, env map[string]string) sandbox.Sandbox {
	t.Helper()
	box, err := Provider{Host: NewHost(DefaultTTL), Scope: t.Name()}.Open(context.Background(), sandbox.Spec{Workspace: t.TempDir(), Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func run(t *testing.T, box sandbox.Sandbox, req sandbox.ExecRequest) (sandbox.Result, string) {
	t.Helper()
	var mu sync.Mutex
	var out strings.Builder
	req.OnChunk = func(b []byte) { mu.Lock(); out.Write(b); mu.Unlock() }
	p, err := box.Exec(context.Background(), req)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	res := p.Wait()
	mu.Lock()
	defer mu.Unlock()
	return res, out.String()
}

// The daemon's re-entry vars must not reach a child: a nested `figaro`
// that inherits _FIGARO_DAEMON re-enters daemon mode and hangs on accept.
func TestEnvDropsDaemonVarsAndKeepsTheRest(t *testing.T) {
	t.Setenv("_FIGARO_DAEMON", "1")
	t.Setenv("HUSH_AGENT_CHILD", "1")
	t.Setenv("HUSH_MANAGED_STATE_DIR", "/somewhere")
	t.Setenv("FIGARO_KEEP_THIS", "keep")

	res, out := run(t, open(t, map[string]string{"FIGARO_SPEC": "spec"}), sandbox.ExecRequest{Command: "env"})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s", res.ExitCode, out)
	}
	for _, d := range []string{"_FIGARO_DAEMON=", "HUSH_AGENT_CHILD=", "HUSH_MANAGED_STATE_DIR="} {
		if strings.Contains(out, d) {
			t.Errorf("child env still contains %q", d)
		}
	}
	for _, want := range []string{"FIGARO_KEEP_THIS=keep", "FIGARO_SPEC=spec"} {
		if !strings.Contains(out, want) {
			t.Errorf("child env lacks %q:\n%s", want, out)
		}
	}
}

func TestIsolationClaimsNothing(t *testing.T) {
	iso := open(t, nil).Isolation()
	if iso.Provider != "local" || iso.FS || iso.Env || iso.Secrets || iso.UID || iso.Net {
		t.Fatalf("local claims isolation it does not have: %+v", iso)
	}
}

// A setsid grandchild escapes the process group and holds the stdio pipe
// open past the kill; without WaitDelay the wait would never return.
func TestTimeoutDrainsLateOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("setsid process-group escape is Unix-specific")
	}
	type result struct {
		res sandbox.Result
		out string
	}
	ch := make(chan result, 1)
	box := open(t, nil)
	go func() {
		res, out := run(t, box, sandbox.ExecRequest{Command: "echo DRAINED; setsid sleep 30 & sleep 30", Timeout: 200 * time.Millisecond})
		ch <- result{res, out}
	}()
	select {
	case r := <-ch:
		if !r.res.TimedOut {
			t.Errorf("expected TimedOut, got %+v", r.res)
		}
		if !strings.Contains(r.out, "DRAINED") {
			t.Errorf("pre-kill output not drained; got %q", r.out)
		}
	case <-time.After(waitDelay + 5*time.Second):
		t.Fatal("wait did not return within the grace window")
	}
}

func TestCancelKillsTheTree(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := open(t, nil).Exec(ctx, sandbox.ExecRequest{Command: "bash -c 'sleep 60' & sleep 60"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-p.Done():
		if res := p.Wait(); !res.Canceled || res.TimedOut {
			t.Fatalf("got %+v, want Canceled", res)
		}
	case <-time.After(waitDelay + 5*time.Second):
		t.Fatal("cancel did not end the process")
	}
}

func TestPTY(t *testing.T) {
	ptyWorks := func() (ok bool) {
		defer func() { recover() }()
		cmd := exec.Command("echo", "probe")
		if _, err := pty.Start(cmd); err != nil {
			return false
		}
		cmd.Wait()
		return true
	}()
	tests := []struct {
		name, command, wantOut string
		forceFail, wantWarn    bool
	}{
		{name: "captures output", command: "echo hello-pty", wantOut: "hello-pty"},
		{name: "child reports a tty", command: "test -t 1 && echo IS_TTY || echo NOT_TTY", wantOut: "IS_TTY"},
		{name: "fallback warns and still runs", command: "echo after-fallback", forceFail: true, wantOut: "after-fallback", wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.forceFail && !ptyWorks {
				t.Skip("pty unavailable")
			}
			if tt.forceFail {
				orig := ptyStart
				ptyStart = func(*exec.Cmd) (*os.File, error) { return nil, fmt.Errorf("forced pty failure") }
				t.Cleanup(func() { ptyStart = orig })
			}
			res, out := run(t, open(t, nil), sandbox.ExecRequest{Command: tt.command, PTY: true})
			if res.ExitCode != 0 {
				t.Fatalf("exit %d, output: %s", res.ExitCode, out)
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Errorf("output %q missing %q", out, tt.wantOut)
			}
			if gotWarn := strings.Contains(out, "PTY spawn failed, fell back to pipe"); gotWarn != tt.wantWarn {
				t.Errorf("warn=%v, want %v; output: %s", gotWarn, tt.wantWarn, out)
			}
		})
	}
}

// A child blocking on a cursor-position request gets a synthetic reply
// instead of hanging until the timeout.
func TestPTYAnswersDSR(t *testing.T) {
	cmd := `stty raw; printf '\033[6n'; dd bs=1 count=1 >/dev/null 2>&1; echo DSR_OK`
	res, out := run(t, open(t, nil), sandbox.ExecRequest{Command: cmd, PTY: true, Timeout: 5 * time.Second})
	if res.TimedOut {
		t.Fatalf("child hung waiting for DSR reply; output: %s", out)
	}
	if !strings.Contains(out, "DSR_OK") {
		t.Errorf("output %q missing DSR_OK", out)
	}
}

func TestUpdateSerializesSamePath(t *testing.T) {
	path := t.TempDir() + "/a.txt"
	if err := os.WriteFile(path, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = FS{}.Update(path, func(old []byte) ([]byte, error) {
				time.Sleep(5 * time.Millisecond)
				n, _ := strconv.Atoi(string(old))
				return []byte(strconv.Itoa(n + 1)), nil
			})
		}()
	}
	wg.Wait()
	got, _ := os.ReadFile(path)
	if string(got) != "8" {
		t.Fatalf("lost an update: file reads %q, want 8", got)
	}
}
