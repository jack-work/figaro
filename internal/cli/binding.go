package cli

import (
	"context"
	"os"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// Binding policy.
//
// The daemon tracks a "pid → aria" binding so an interactive shell can
// `attend <id>` once and have subsequent verbs default to that aria.
// Non-interactive callers (scripts, an aria's own bash tool) should NOT
// look up or mutate that binding — every command must name its target
// explicitly via --id / <id>. Silent inheritance across a `figaro send
// -f` into a subshell into another `figaro` invocation has caused real
// bugs (a fuzz run's child figaro grabbed its parent's binding and
// forked the wrong aria).
//
// bindingDisabled reports whether this invocation is in absolute mode:
// no Resolve, no Bind, no Unbind. Triggered by:
//
//   - --no-bind / --absolute on the command line
//   - FIGARO_NO_BIND=1 in the environment
//   - non-interactive stdin AND non-interactive stderr (best-effort;
//     err on the side of no-bind so scripts default to safe)
//
// A TTY on either stdin OR stderr is enough to opt into binding
// (covers piped-stdout scripting from a real terminal like
// `figaro send ... | jq`).
var (
	noBindFlag  bool // set by extractNoBindFlag
	forceBind   bool // reserved: --bind override (currently unused)
	noBindEnv   bool // set at Run start from FIGARO_NO_BIND
	interactive bool // set at Run start; true when at least one std stream is a TTY
)

// initBindingPolicy computes the interactive/env state once at Run.
// Called before the router runs so the policy is stable across a call.
func initBindingPolicy() {
	noBindEnv = envTruthy(os.Getenv("FIGARO_NO_BIND"))
	interactive = term.IsTerminal(int(os.Stdin.Fd())) ||
		term.IsTerminal(int(os.Stderr.Fd()))
}

// bindingDisabled is the single query every CLI helper consults before
// touching the pid-binding.
func bindingDisabled() bool {
	if forceBind {
		return false
	}
	if noBindFlag || noBindEnv {
		return true
	}
	return !interactive
}

// extractNoBindFlag removes --no-bind / --absolute / -A from args in
// place and returns the filtered slice, setting noBindFlag if found.
// Runs before router dispatch so any command can be called with the
// flag regardless of its own flag definitions. Also recognizes --bind
// as an explicit opt-in override.
func extractNoBindFlag(args []string) []string {
	out := args[:0]
	for _, a := range args {
		switch a {
		case "--no-bind", "--absolute", "-A":
			noBindFlag = true
			continue
		case "--bind":
			forceBind = true
			continue
		}
		out = append(out, a)
	}
	// Zero the tail so the pointer array doesn't retain stale strings.
	for i := len(out); i < len(args); i++ {
		args[i] = ""
	}
	return out
}

func envTruthy(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	}
	return false
}

// resolveBinding wraps acli.Resolve with the binding policy: returns
// a not-found response (no error) when binding is disabled, so callers
// can uniformly treat the absent case as "nothing bound."
func resolveBinding(ctx context.Context, acli *angelus.Client, ppid int) (*rpc.ResolveResponse, error) {
	if bindingDisabled() {
		return &rpc.ResolveResponse{Found: false}, nil
	}
	return acli.Resolve(ctx, ppid)
}

// bindBinding wraps acli.Bind — no-op under bindingDisabled.
func bindBinding(ctx context.Context, acli *angelus.Client, ppid int, figaroID string, atLT uint64) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Bind(ctx, ppid, figaroID, atLT)
}

// unbindBinding wraps acli.Unbind — no-op under bindingDisabled.
func unbindBinding(ctx context.Context, acli *angelus.Client, ppid int) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Unbind(ctx, ppid)
}
