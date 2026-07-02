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

// hasPreDashFlag reports whether any of names appears in args before a
// `--` boundary. Used by PassRaw commands to scan for flags that would
// otherwise be swallowed by the raw-args pipeline.
func hasPreDashFlag(args []string, names ...string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
}

// die prints to stderr and exits 1.
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
