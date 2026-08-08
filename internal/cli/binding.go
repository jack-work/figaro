package cli

import (
	"context"
	"os"
	"strings"

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
	forceBind   bool // --bind: bind even when no stream is a TTY
	noBindEnv   bool // set at Run start from FIGARO_NO_BIND
	interactive bool // set at Run start; true when at least one std stream is a TTY
	shellPID    int  // set at Run start; the process this shell's binding is keyed to
)

// initBindingPolicy computes the interactive/env state once at Run.
// Called before the router runs so the policy is stable across a call.
func initBindingPolicy() {
	noBindEnv = envTruthy(os.Getenv("FIGARO_NO_BIND"))
	shellPID = shellKey()
	interactive = term.IsTerminal(int(os.Stdin.Fd())) ||
		term.IsTerminal(int(os.Stderr.Fd()))
	// The same signal arms the DUKE placeholder: only a human at a terminal
	// speaks for the end user, so an aria's own shell-out — never a TTY —
	// cannot present itself as its master by accident.
	rpc.SetInteractive(interactive)
}

// envAriaID returns the aria id pinned by FIGARO_ARIA, or "" if unset
// or malformed.
//
// An agent injects FIGARO_ARIA=<its own id> into every bash tool
// invocation, so a shell-out is *statically attended* to the aria that
// spawned it: `figaro state`, `figaro set`, `figaro status`, `figaro
// show` and a bare `figaro send` all target that aria with no --id and
// no pid binding involved.
//
// It is an identity, not a binding. Nothing is written to the angelus,
// and it cannot be changed from inside — `figaro attend` refuses, and
// bind/unbind are no-ops. Addressing another aria takes an explicit
// --id.
//
// Precedence: --id  >  FIGARO_ARIA  >  pid binding.
func envAriaID() string {
	id := strings.TrimSpace(os.Getenv("FIGARO_ARIA"))
	if id == "" {
		return ""
	}
	if err := rpc.ValidateAriaID(id); err != nil {
		return ""
	}
	return id
}

// bindingDisabled is the query every CLI helper consults before MUTATING
// the pid-binding.
func bindingDisabled() bool {
	// A statically-attended shell has no binding to mutate: its identity
	// came in over the environment and outranks even --bind.
	if envAriaID() != "" {
		return true
	}
	if forceBind {
		return false
	}
	if noBindFlag || noBindEnv {
		return true
	}
	return !interactive
}

// resolveDisabled is the same question for READING the binding, and it
// differs in one rung: a missing TTY does not hide the answer.
//
// Asking which aria a shell attends is not a mutation. The answer is
// already public — `figaro list` prints it — and the hazard the tty rung
// was added for (a nested figaro inheriting its parent's binding) is now
// answered by identity: an agent's children carry FIGARO_ARIA, which
// outranks everything here. What the rung actually cost was every
// legitimate reader that redirects, which is all of them: prompt
// segments, statuslines, `fig status | jq`.
//
// --no-bind still hides it. "Absolute mode" means address explicitly, and
// that is a stated intent rather than a guess about the terminal.
func resolveDisabled() bool {
	if envAriaID() != "" {
		return true
	}
	if forceBind {
		return false
	}
	return noBindFlag || noBindEnv
}

// extractNoBindFlag pulls the binding-policy flags out of argv and returns
// the rest. It stops at the first bare `--`: everything after that boundary
// is a prompt, and a prompt is not argv.
//
// It used to scan the whole slice, so `figaro send -- explain --no-bind`
// deleted the token from the PROMPT and flipped the policy on the way past.
// Two silent corruptions from one loop, and the same class as every other
// defect on this surface: argv nobody consumed must not vanish.
func extractNoBindFlag(args []string) []string {
	out := args[:0]
	pastBoundary := false
	for _, a := range args {
		if pastBoundary {
			out = append(out, a)
			continue
		}
		if a == "--" {
			pastBoundary = true
			out = append(out, a)
			continue
		}
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
//
// FIGARO_ARIA short-circuits the pid map entirely (see envAriaID).
func resolveBinding(ctx context.Context, acli *angelus.Client, ppid int) (*rpc.ResolveResponse, error) {
	if id := envAriaID(); id != "" {
		return resolveEnvAria(ctx, acli, id)
	}
	if resolveDisabled() {
		return &rpc.ResolveResponse{Found: false}, nil
	}
	return acli.Resolve(ctx, ppid)
}

// resolveEnvAria answers the FIGARO_ARIA path. Attach — not Bind —
// hands back the endpoint and revives a dormant aria without touching
// the pid map; in the common case (an aria's own shell-out, so the
// aria is live) it is a registry lookup on the daemon side.
//
// A dangling id (the aria was killed) reports not-found rather than
// erroring, so callers keep their uniform "nothing bound" branch.
func resolveEnvAria(ctx context.Context, acli *angelus.Client, id string) (*rpc.ResolveResponse, error) {
	resp, err := acli.Attach(ctx, id)
	if err != nil {
		return &rpc.ResolveResponse{Found: false}, nil
	}
	return &rpc.ResolveResponse{
		FigaroID: id,
		Endpoint: resp.Endpoint,
		Found:    true,
	}, nil
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
