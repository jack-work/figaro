package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// Binding policy.
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
	// speaks for the end user, so an aria's own shell-out: never a TTY -
	// cannot present itself as its master by accident.
	rpc.SetInteractive(interactive)
}

// envAriaID returns the aria id pinned by FIGARO_ARIA, or "" if unset
// or malformed.
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
func resolveBinding(ctx context.Context, acli *sdk.Angelus, ppid int) (*rpc.ResolveResponse, error) {
	if id := envAriaID(); id != "" {
		return resolveEnvAria(ctx, acli, id)
	}
	if resolveDisabled() {
		return &rpc.ResolveResponse{Found: false}, nil
	}
	return acli.Resolve(ctx, ppid)
}

// resolveEnvAria answers the FIGARO_ARIA path. Attach: not Bind -
// hands back the endpoint and revives a dormant aria without touching
// the pid map; in the common case (an aria's own shell-out, so the
// aria is live) it is a registry lookup on the daemon side.
func resolveEnvAria(ctx context.Context, acli *sdk.Angelus, id string) (*rpc.ResolveResponse, error) {
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

// bindBinding wraps acli.Bind: no-op under bindingDisabled.
func bindBinding(ctx context.Context, acli *sdk.Angelus, ppid int, figaroID string, atLT uint64) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Bind(ctx, ppid, figaroID, atLT)
}

// unbindBinding wraps acli.Unbind: no-op under bindingDisabled.
func unbindBinding(ctx context.Context, acli *sdk.Angelus, ppid int) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Unbind(ctx, ppid)
}
