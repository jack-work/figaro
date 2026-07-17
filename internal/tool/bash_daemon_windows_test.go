//go:build windows

package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestDaemonLikeBash(t *testing.T) {
	const helperEnv = "FIGARO_DAEMON_BASH_HELPER"
	const resultEnv = "FIGARO_DAEMON_BASH_RESULT"

	if os.Getenv(helperEnv) == "1" {
		result, err := NewBashTool(os.TempDir()).Execute(context.Background(), map[string]interface{}{
			"command": "printf FIGARO_DAEMON_BASH_OK",
		}, nil)
		payload, _ := json.Marshal(struct {
			Output string `json:"output"`
			Error  string `json:"error"`
		}{
			Output: textFromContent(result),
			Error:  errorString(err),
		})
		_ = os.WriteFile(os.Getenv(resultEnv), payload, 0o600)
		return
	}

	resultPath := filepath.Join(t.TempDir(), "result.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonLikeBash$")
	cmd.Env = append(os.Environ(), helperEnv+"=1", resultEnv+"="+resultPath)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Run(); err != nil {
		t.Fatalf("daemon-like helper: %v", err)
	}

	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	var result struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse helper result: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("daemon-like bash error: %s", result.Error)
	}
	if result.Output != "FIGARO_DAEMON_BASH_OK" {
		t.Fatalf("daemon-like bash output: %q", result.Output)
	}
}

func textFromContent(content []message.Content) string {
	var text string
	for _, item := range content {
		if item.Type == message.ContentProse {
			text += item.Text
		}
	}
	return text
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
