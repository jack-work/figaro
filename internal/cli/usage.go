package cli

import (
	"fmt"
	"os"
	"strings"
)

// extractPrompt pulls the prompt text out of an argv tail of the form
// `... -- words words words`. Returns "" if no `--` separator is found
// or if there are no words after it.
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

// die prints to stderr and exits 1. The message is the same for all
// fatal CLI paths (config load, RPC failures, missing args).
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
