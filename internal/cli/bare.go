package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

// The bare prompt form: `figaro [send-flags] -- <prompt>`.

// hasDashBoundary reports whether argv contains a bare `--` token: the
// boundary that turns an invocation into a prompt.
func hasDashBoundary(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return true
		}
	}
	return false
}

// isBareForm reports whether argv should be handled as the bare prompt
// form rather than dispatched to the router: a `--` boundary is present
// and the first token does not name a command. isCommand is the router's
// HasCommand (threaded as a func so the decision is testable on its own).
func isBareForm(args []string, isCommand func(string) bool) bool {
	return len(args) > 0 && hasDashBoundary(args) && !isCommand(args[0])
}

// runBarePrompt handles `figaro [send-flags] -- <prompt>`: the same
// parse, the same dispatch, the same semantics as `figaro send`.
// Caller has already established that args[0] is not a known command.
func runBarePrompt(progName string, router *cmdkit.Router, loaded *config.Loaded, args []string) {
	if first := args[0]; first != "--" && !strings.HasPrefix(first, "-") {
		unknownBareCommand(progName, router, first)
	}
	runSendAs(loaded, progName, args)
}

// unknownBareCommand reports a leading bare word that is neither a command
// nor something the bare prompt form accepts, and exits. It mirrors the
// router's own did-you-mean, and points at the two ways to mean an aria.
func unknownBareCommand(progName string, router *cmdkit.Router, word string) {
	fmt.Fprintf(stderrw, "error: unknown command %q\n", word)
	if s := router.Suggest(word); s != "" {
		fmt.Fprintf(stderrw, "  did you mean: %s %s\n", progName, s)
	}
	fmt.Fprintf(stderrw, "  to prompt an aria:  %s --id %s -- <prompt>\n", progName, word)
	fmt.Fprintf(stderrw, "  or the explicit verb: %s send %s -- <prompt>\n", progName, word)
	// Misuse, not failure: the router answers an unknown command with 2, and
	// the bare form is the same mistake reached by a different door. exitNow,
	// not exitProcess: an abort unwinds to its boundary so the hooks run and an
	// in-process caller is not killed by it.
	exitNow(2)
}
