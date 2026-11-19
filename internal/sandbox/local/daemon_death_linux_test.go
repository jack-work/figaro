//go:build linux

package local

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/sandbox"
)

const holderEnv = "FIGARO_TEST_TOOL_HOLDER_PIDFILE"

// Not a test: the daemon TestToolsDieWithTheirDaemon SIGKILLs mid-tool.
func TestToolHolderHelper(t *testing.T) {
	pidfile := os.Getenv(holderEnv)
	if pidfile == "" {
		t.Skip("helper process only")
	}
	_, err := open(t, nil).Exec(t.Context(), sandbox.ExecRequest{
		Command: "echo $$ > " + pidfile + ".tmp && mv " + pidfile + ".tmp " + pidfile + " && exec sleep 30",
		PTY:     os.Getenv("FIGARO_TEST_TOOL_HOLDER_PTY") != "",
	})
	if err != nil {
		os.Exit(2)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// Issue #137: a tool's process kept running after its daemon was SIGKILLed.
func TestToolsDieWithTheirDaemon(t *testing.T) {
	for _, pty := range []bool{false, true} {
		t.Run("pty="+strconv.FormatBool(pty), func(t *testing.T) {
			pidfile := t.TempDir() + "/tool.pid"
			holder := exec.Command(os.Args[0], "-test.run=^TestToolHolderHelper$")
			holder.Env = append(os.Environ(), holderEnv+"="+pidfile)
			if pty {
				holder.Env = append(holder.Env, "FIGARO_TEST_TOOL_HOLDER_PTY=1")
			}
			if err := holder.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { holder.Process.Kill(); holder.Wait() })
			tool := 0
			for deadline := time.Now().Add(10 * time.Second); tool == 0; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the holder never started its tool")
				}
				b, _ := os.ReadFile(pidfile)
				tool, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			}
			t.Cleanup(func() { syscall.Kill(tool, syscall.SIGKILL) })
			holder.Process.Signal(syscall.SIGKILL)
			holder.Wait()
			for deadline := time.Now().Add(5 * time.Second); alive(tool); time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("tool %d outlived its SIGKILLed daemon", tool)
				}
			}
		})
	}
}

func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
}
