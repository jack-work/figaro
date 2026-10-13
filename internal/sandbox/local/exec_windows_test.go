//go:build windows

package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jack-work/figaro/api/sandbox"
)

func TestDaemonLikeBash(t *testing.T) {
	const helperEnv = "FIGARO_DAEMON_BASH_HELPER"
	const resultEnv = "FIGARO_DAEMON_BASH_RESULT"

	if os.Getenv(helperEnv) == "1" {
		box, err := Provider{Host: NewHost(DefaultTTL)}.Open(context.Background(), sandbox.Spec{Workspace: os.TempDir()})
		if err != nil {
			_ = os.WriteFile(os.Getenv(resultEnv), []byte("ERR "+err.Error()), 0o600)
			return
		}
		_, out := run(t, box, sandbox.ExecRequest{Command: "printf FIGARO_DAEMON_BASH_OK"})
		_ = os.WriteFile(os.Getenv(resultEnv), []byte(out), 0o600)
		return
	}

	resultPath := filepath.Join(t.TempDir(), "result.txt")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonLikeBash$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", resultEnv+"="+resultPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Run(); err != nil {
		t.Fatalf("daemon-like helper: %v", err)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	if strings.TrimSpace(string(data)) != "FIGARO_DAEMON_BASH_OK" {
		t.Fatalf("daemon-like bash output: %q", data)
	}
}

func TestReleasesWindowsJobHandles(t *testing.T) {
	box := open(t, nil)
	for i := 0; i < 32; i++ {
		if _, out := run(t, box, sandbox.ExecRequest{Command: "printf FIGARO_JOB_CLEANUP"}); out != "FIGARO_JOB_CLEANUP" {
			t.Fatalf("run %d output: %q", i, out)
		}
	}
	jobsMu.Lock()
	count := len(jobs)
	jobsMu.Unlock()
	if count != 0 {
		t.Fatalf("leaked %d Windows Job handle(s)", count)
	}
}
