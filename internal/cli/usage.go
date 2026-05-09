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

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: figaro -- <prompt>                    (shorthand for `figaro aria`)")
	fmt.Fprintln(os.Stderr, "       figaro aria [<id>] -- <prompt>        (prompt the named or pid-bound aria; create if absent)")
	fmt.Fprintln(os.Stderr, "       figaro aria [<id>] [N] [-v] [-l] [-a] (render history; default last 10)")
	fmt.Fprintln(os.Stderr, "       figaro qua  [<id>] -- <prompt>        (alias of `figaro aria` for prompting)")
	fmt.Fprintln(os.Stderr, "       figaro new -- <prompt>                (fresh aria with server-generated id, bind pid)")
	fmt.Fprintln(os.Stderr, "       figaro plain -- <prompt>              (raw, ephemeral, pipe-friendly; also 'l')")
	fmt.Fprintln(os.Stderr, "       figaro x [-n|-y] -- <instruction>     (ask figaro to write bash and exec it locally)")
	fmt.Fprintln(os.Stderr, "       figaro attend <id>")
	fmt.Fprintln(os.Stderr, "       figaro detach")
	fmt.Fprintln(os.Stderr, "       figaro context [id]")
	fmt.Fprintln(os.Stderr, "       figaro rehydrate [--dry-run]")
	fmt.Fprintln(os.Stderr, "       figaro set <key> <value>              (chalkboard patch, no LLM round-trip)")
	fmt.Fprintln(os.Stderr, "       figaro unset <key> [<key>...]")
	fmt.Fprintln(os.Stderr, "       figaro chalkboard                     (current snapshot of bound figaro)")
	fmt.Fprintln(os.Stderr, "       figaro -s <alias> [-json]             (read a registered DurableDerivation; e.g. usage)")
	fmt.Fprintln(os.Stderr, "       figaro list")
	fmt.Fprintln(os.Stderr, "       figaro kill <id>")
	fmt.Fprintln(os.Stderr, "       figaro models")
	fmt.Fprintln(os.Stderr, "       figaro rest")
	fmt.Fprintln(os.Stderr, "       figaro login <provider>")
}

// die prints to stderr and exits 1. The message is the same for all
// fatal CLI paths (config load, RPC failures, missing args).
func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
