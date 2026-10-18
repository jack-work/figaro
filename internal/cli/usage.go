package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

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

const exitInterrupted = 130 // 128 + SIGINT

const exitTurnFailed = 1

// stdout and stderrw are variables so a verb run in-process (pager command mode)
// is capturable. The incipit and the pager write to os.Stdout directly: their
// output is the terminal, not a result.
var stdout io.Writer = os.Stdout

var stderrw io.Writer = os.Stderr

// exitProcess is reached only from exitAt; a variable so a test can watch it.
var exitProcess = os.Exit

// exitPanic is how a verb aborts: die panics with one and the nearest boundary
// (cli.Run exits for real, routeCaptured returns the code) decides what that means.
// A die on a goroutine with no boundary above it panics the process.
type exitPanic int

// exitHooks run before any exit: os.Exit runs no defers, and leaving the alt
// screen is a defer.
var (
	exitMu    sync.Mutex
	exitHooks []func()
)

func atExit(f func()) {
	exitMu.Lock()
	defer exitMu.Unlock()
	exitHooks = append(exitHooks, f)
}

// Every exit that is not a normal return goes through exitNow.
func exitNow(code int) { panic(exitPanic(code)) }

func exitAt(code int) {
	exitMu.Lock()
	hooks := exitHooks
	exitHooks = nil
	exitMu.Unlock()
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i]()
	}
	exitProcess(code)
}

// RecoverExit, deferred by cli.Run, turns a verb's die into a real exit with the
// hooks run.
func RecoverExit() {
	if rec := recover(); rec != nil {
		if code, ok := rec.(exitPanic); ok {
			exitAt(int(code))
			return
		}
		panic(rec)
	}
}

// die is a runtime failure: exit 1. dieUsage is rejected argv: exit 2, as the
// router answers.
func die(format string, args ...interface{}) {
	fmt.Fprintf(stderrw, "error: "+format+"\n", args...)
	exitNow(1)
}

func dieUsage(format string, args ...interface{}) {
	fmt.Fprintf(stderrw, "error: "+format+"\n", args...)
	exitNow(2)
}
