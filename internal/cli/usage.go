package cli

import (
	"fmt"
	"io"
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

// exitInterrupted is the shell's convention for "killed by SIGINT"
// (128 + SIGINT). plainPrompt has always returned it; the interactive
// stream used to exit 0 after a Ctrl-C, which told every caller that an
// abandoned turn had succeeded.
const exitInterrupted = 130

// exitProcess is os.Exit, indirected so the die helpers are testable.
// Tests swap it for a recorder; nothing else may touch it.
// stdout is where a command's ORDINARY OUTPUT goes -- the thing it was asked
// to produce, as opposed to a diagnostic. It is a variable for the same reason
// exitProcess is: a verb run in-process (command mode inside the pager) must be
// capturable, and 156 call sites writing to os.Stdout directly were not.
//
// It is NOT the renderer's writer. The incipit and the pager write to os.Stdout
// explicitly, because their output is the terminal itself rather than a result,
// and redirecting it would capture the escape sequences that paint the screen.
var stdout io.Writer = os.Stdout

// stderrw is the diagnostic twin of stdout: die(), usage and every "warning:"
// line. Command mode captures it too, because a verb's failure text is the
// thing the reader most needs and it was going to the terminal underneath the
// alt screen -- i.e. nowhere.
var stderrw io.Writer = os.Stderr

// exitProcess ends the process for real. It is reached from exactly one place
// (exitAt, below) and is a variable only so a test can watch it.
var exitProcess = os.Exit

// exitPanic is how a verb ABORTS: die() panics with one of these and the
// nearest boundary decides what an abort means there.
//
// It used to be a global swapped in and out around each in-process command,
// which is a process-wide mutation to achieve a per-call effect -- and one that
// a second concurrent command would have silently undone. Now the abort is
// always a panic and the BOUNDARY differs instead:
//
//	cli.Run          recovers it, runs the exit hooks, and exits for real
//	routeCaptured    recovers it and returns the code as a command's result
//
// die() is already the one chokepoint every verb reports failure through -- 185
// call sites across 85 functions -- so making the chokepoint unwind costs
// nothing at those sites, where making each of them RETURN an error would cost
// a signature change all the way up.
//
// THE RISK, stated: a die() on a goroutine with no boundary above it now panics
// the process where it used to exit cleanly. Both are fatal; this one is
// louder, and prints a stack that names the offender.
type exitPanic int

// exitHooks are the things that MUST happen before the process goes, even on
// the paths that never unwind: os.Exit does not run defers, and the pager's
// `defer lt.leaveTranscript()` is exactly the kind of defer that matters. A
// die() with the transcript up therefore left the user on the alternate
// screen: shell invisible, cursor hidden, autowrap off: with the error
// message painted on a buffer about to be abandoned. `figaro send -l` whose
// Qua fails is that path today (stream.go dies after --listen has opened the
// pager), and so is the Ctrl-C 130 exit.
var exitHooks []func()

func atExit(f func()) { exitHooks = append(exitHooks, f) }

// exitNow runs the hooks and exits. Every exit that is not a normal return
// goes through here; exitProcess stays the raw primitive underneath it.
func exitNow(code int) { panic(exitPanic(code)) }

// exitAt is the boundary: run the hooks that must happen before the process
// goes (os.Exit runs no defers, and `defer lt.leaveTranscript()` is exactly the
// defer that matters), then go.
func exitAt(code int) {
	for i := len(exitHooks) - 1; i >= 0; i-- {
		exitHooks[i]()
	}
	exitHooks = nil
	exitProcess(code)
}

// recoverExit is the boundary's half of the abort. Deferred by cli.Run, it
// turns a verb's die() into a real exit with the hooks run.
func recoverExit() {
	if rec := recover(); rec != nil {
		if code, ok := rec.(exitPanic); ok {
			exitAt(int(code))
			return
		}
		panic(rec)
	}
}

// die reports a RUNTIME failure (called correctly, did not succeed): exit 1.
// dieUsage is for rejected argv: exit 2. Before the split, the same mistake
// answered 2 from the router and 1 from the hand-rolled parsers, a seam no
// user can see. clig.dev: 1 = general error, 2 = misuse.
func die(format string, args ...interface{}) {
	fmt.Fprintf(stderrw, "error: "+format+"\n", args...)
	exitNow(1)
}

// dieUsage reports that ARGV WAS REJECTED, an unknown flag, a missing or
// malformed target, contradictory flags, a prompt in the wrong place, and
// exits 2, matching what the router returns for the same class of mistake.
func dieUsage(format string, args ...interface{}) {
	fmt.Fprintf(stderrw, "error: "+format+"\n", args...)
	exitNow(2)
}
