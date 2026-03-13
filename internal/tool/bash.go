package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

type Bash struct{ Cwd string }

func (b *Bash) Name() string        { return "bash" }
func (b *Bash) Description() string { return "Execute a bash command. Returns stdout and stderr." }
func (b *Bash) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string", "description": "Bash command to execute"},
		},
		"required": []string{"command"},
	}
}

func (b *Bash) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = b.Cwd
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	output := out.String()
	if output == "" {
		output = "(no output)"
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("%s\n\nCommand exited with code %d", output, exitErr.ExitCode()), nil
		}
		return "", err
	}
	return output, nil
}
