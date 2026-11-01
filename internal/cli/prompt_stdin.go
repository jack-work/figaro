package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var errStdinAndArgv = errors.New("--stdin reads the prompt from stdin: give no prompt after `--`")

func promptFromStdin(opts sendOpts, rest []string, in io.Reader) (string, error) {
	if !opts.stdin {
		return extractPrompt(rest), nil
	}
	if hasDashBoundary(rest) {
		return "", errStdinAndArgv
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("--stdin: %w", err)
	}
	prompt := strings.TrimRight(string(b), "\n")
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("--stdin: stdin held no prompt")
	}
	return prompt, nil
}
