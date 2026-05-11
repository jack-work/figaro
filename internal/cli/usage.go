package cli

import (
	"fmt"
	"os"
	"strings"
)

// extractPrompt extracts the prompt after `--` in argv.
func extractPrompt(args []string) string {
	for i, arg := range args {
		if arg == "--" {
			rest := args[i+1:]
			if len(rest) == 0 {
				return ""
			}
			return strings.Join(rest, " ")
		}
	}
	return ""
}

// die prints to stderr and exits 1.
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
