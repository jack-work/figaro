package cli

import (
	"context"
	"github.com/jack-work/figaro/sdk"
	"os"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

var (
	noBindFlag  bool
	forceBind   bool // --bind: bind even when no stream is a TTY
	noBindEnv   bool // set at Run start from FIGARO_NO_BIND
	interactive bool // set at Run start; true when at least one std stream is a TTY
	shellPID    int  // set at Run start; the process this shell's binding is keyed to
)

// Computed once before the router runs, so the policy is stable across a call.
func initBindingPolicy() {
	noBindEnv = envTruthy(os.Getenv("FIGARO_NO_BIND"))
	shellPID = shellKey()
	interactive = term.IsTerminal(int(os.Stdin.Fd())) ||
		term.IsTerminal(int(os.Stderr.Fd()))
	// Only a human at a terminal speaks for the end user; an aria's shell-out
	// is never a TTY.
	rpc.SetInteractive(interactive)
}

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

// Gates mutating the binding.
func bindingDisabled() bool {
	// FIGARO_ARIA outranks even --bind.
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

// Gates reading the binding: a missing TTY does not hide the answer.
func resolveDisabled() bool {
	if envAriaID() != "" {
		return true
	}
	if forceBind {
		return false
	}
	return noBindFlag || noBindEnv
}

// Stops at the first bare `--`: everything after it is a prompt.
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

// Disabled binding reads as not found, no error.
func resolveBinding(ctx context.Context, acli *sdk.Angelus, ppid int) (*rpc.ResolveResponse, error) {
	if id := envAriaID(); id != "" {
		return resolveEnvAria(ctx, acli, id)
	}
	if resolveDisabled() {
		return &rpc.ResolveResponse{Found: false}, nil
	}
	return acli.Resolve(ctx, ppid)
}

// Attach, not Bind: revives a dormant aria without touching the pid map.
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

func bindBinding(ctx context.Context, acli *sdk.Angelus, ppid int, figaroID string, atLT uint64) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Bind(ctx, ppid, figaroID, atLT)
}

func unbindBinding(ctx context.Context, acli *sdk.Angelus, ppid int) error {
	if bindingDisabled() {
		return nil
	}
	return acli.Unbind(ctx, ppid)
}
