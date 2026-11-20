package cli

import (
	"errors"
	"strings"
)

var errEmptyStdin = errors.New("--stdin: stdin held no prompt")

func blankStdin(d dressing, prompt string) bool {
	return d.exact && strings.TrimSpace(prompt) == "" && len(d.images) == 0
}
