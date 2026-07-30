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

// preDashFlagValue extracts the value of a string-valued flag before the
// `--` boundary. Handles both `--name value` and `--name=value` forms
// (and any short aliases in names). Returns ("", false) if the flag is
// absent; returns an error if it appears without a value.
func preDashFlagValue(args []string, names ...string) (string, bool, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", false, nil
		}
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || args[i+1] == "--" {
					return "", false, fmt.Errorf("%s requires a value", n)
				}
				return args[i+1], true, nil
			}
			if strings.HasPrefix(a, n+"=") {
				v := strings.TrimPrefix(a, n+"=")
				if v == "" {
					return "", false, fmt.Errorf("%s requires a value", n)
				}
				return v, true, nil
			}
		}
	}
	return "", false, nil
}

// exitInterrupted is the shell's convention for "killed by SIGINT"
// (128 + SIGINT). plainPrompt has always returned it; the interactive
// stream used to exit 0 after a Ctrl-C, which told every caller that an
// abandoned turn had succeeded.
const exitInterrupted = 130

// exitProcess is os.Exit, indirected so the die helpers are testable.
// Tests swap it for a recorder; nothing else may touch it.
var exitProcess = os.Exit

// die reports a RUNTIME failure (called correctly, did not succeed): exit 1.
// dieUsage is for rejected argv: exit 2. Before the split, the same mistake
// answered 2 from the router and 1 from the hand-rolled parsers — a seam no
// user can see. clig.dev: 1 = general error, 2 = misuse.
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	exitProcess(1)
}

// dieUsage reports that ARGV WAS REJECTED — an unknown flag, a missing or
// malformed target, contradictory flags, a prompt in the wrong place — and
// exits 2, matching what the router returns for the same class of mistake.
func dieUsage(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	exitProcess(2)
}
