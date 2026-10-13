package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
)

// The bare prompt form: `figaro [send-flags] -- <prompt>`.

func hasDashBoundary(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return true
		}
	}
	return false
}

func isBareForm(args []string, isCommand func(string) bool) bool {
	return len(args) > 0 && hasDashBoundary(args) && !isCommand(args[0])
}

func runBarePrompt(progName string, router *cmdkit.Router, loaded *config.Loaded, args []string) {
	if first := args[0]; first != "--" && !strings.HasPrefix(first, "-") {
		unknownBareCommand(progName, router, first)
	}
	runSendAs(loaded, progName, args)
}

func unknownBareCommand(progName string, router *cmdkit.Router, word string) {
	fmt.Fprintf(stderrw, "error: unknown command %q\n", word)
	if s := router.Suggest(word); s != "" {
		fmt.Fprintf(stderrw, "  did you mean: %s %s\n", progName, s)
	}
	fmt.Fprintf(stderrw, "  to prompt an aria:  %s --id %s -- <prompt>\n", progName, word)
	fmt.Fprintf(stderrw, "  or the explicit verb: %s send %s -- <prompt>\n", progName, word)
	// exitNow, not exitProcess: the abort unwinds so hooks run and an
	// in-process caller survives.
	exitNow(2)
}
