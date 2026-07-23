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

// die prints to stderr and exits 1.
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
