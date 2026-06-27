package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
)

// runList prints the conversation forest (live, dormant, and frozen fork
// points), ordered as a depth-first walk of the fork tree so each trunk's
// lineage reads top-to-bottom. The leading VECTOR column (0, 0.0, 0.1, …)
// shows fork depth + branch; MANTRA is the thread's essence. With jsonOut
// the raw entries are emitted instead.
func runList(loaded *config.Loaded, jsonOut bool) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, err := acli.List(ctx)
		if err != nil {
			die("list: %s", err)
		}

		// Depth-first preorder by vector: component-wise compare, so
		// 0 < 0.0 < 0.0.0 < 0.1 < 0.1.0. Vectorless entries sink last.
		figs := resp.Figaros
		sort.SliceStable(figs, func(i, j int) bool {
			return vectorLess(figs[i].Vector, figs[j].Vector)
		})

		if jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(figs); err != nil {
				die("list --json: %s", err)
			}
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "\tVECTOR\tMANTRA\tID\tSTATE\tAGE\tMODEL\tMSGS\tCONTEXT\tCACHE\tPIDS\tCWD\n")
		for _, f := range figs {
			pids := make([]string, len(f.BoundPIDs))
			for i, p := range f.BoundPIDs {
				pids[i] = fmt.Sprintf("%d", p)
			}
			pidStr := strings.Join(pids, ",")
			if pidStr == "" {
				pidStr = "-"
			}
			ctxStr := "-"
			if f.ContextTokens > 0 {
				ctxStr = fmt.Sprintf("%dk", f.ContextTokens/1000)
				if !f.ContextExact {
					ctxStr = "~" + ctxStr
				}
			}
			cacheStr := "-"
			if f.CacheReadTokens > 0 || f.CacheWriteTokens > 0 {
				cacheStr = fmt.Sprintf("%dk/%dk", f.CacheReadTokens/1000, f.CacheWriteTokens/1000)
			}
			model := f.Model
			if model == "" {
				model = "-"
			}
			current := ""
			if slices.Contains(f.BoundPIDs, os.Getppid()) {
				current = "*"
			}
			// Indent the mantra by fork depth so the limb structure reads
			// visually alongside the dotted vector.
			depth := 0
			if len(f.Vector) > 0 {
				depth = len(f.Vector) - 1
			}
			mantra := strings.Repeat("  ", depth) + dash(f.Mantra)
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				current, vectorString(f.Vector), mantra, f.ID, f.State, relAge(f.LastActive),
				model, f.MessageCount, ctxStr, cacheStr, pidStr, shortCwd(f.Cwd))
		}
		w.Flush()
		return nil
	})
}

// vectorString renders a fork vector as a dotted path ("0.1.0"); "-" if empty.
func vectorString(v []int) string {
	if len(v) == 0 {
		return "-"
	}
	parts := make([]string, len(v))
	for i, c := range v {
		parts[i] = strconv.Itoa(c)
	}
	return strings.Join(parts, ".")
}

// vectorLess orders fork vectors as a depth-first preorder; an empty
// vector sorts after any non-empty one.
func vectorLess(a, b []int) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) != 0 // non-empty before empty
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// dash returns "-" for an empty string.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// relAge renders a unix-millis timestamp as a compact age (e.g. "4m", "2h",
// "3d"); "-" when unknown.
func relAge(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// shortCwd shortens a path for the table: $HOME → ~, then keep the tail if
// it's long. "-" when empty.
func shortCwd(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		p = "~" + strings.TrimPrefix(p, home)
	}
	const max = 28
	if len(p) > max {
		p = "…" + p[len(p)-max+1:]
	}
	return p
}

func runKill(loaded *config.Loaded, idFlag string, args []string) {
	ariaID := idFlag
	if ariaID == "" && len(args) > 0 {
		ariaID = args[0]
	}
	if ariaID == "" {
		die("usage: figaro kill [--id <id> | <id>]")
	}
	runKillByID(loaded, ariaID)
}

func runKillByID(loaded *config.Loaded, figaroID string) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := acli.Kill(ctx, figaroID); err != nil {
			die("kill: %s", err)
		}
		fmt.Fprintf(os.Stderr, "killed %s\n", figaroID)
		return nil
	})
}

// runFork branches a conversation. The target freezes (keeps its id as
// an index node) and two fresh children are minted: the continuation
// (the original line) and an empty alternative.
//
// Target forms: bare (the shell-bound aria), `<id>`, or `<id>:<LT>` for
// an interior fork at that IR logical time (history below <LT> is shared;
// the original suffix becomes the continuation).
//
// Rescoping: when you fork your OWN bound aria, the shell rebinds to the
// continuation so work carries on seamlessly (same trunk/mantra, new id)
// — the bound aria froze, so you must move. Forking any OTHER aria, or
// passing --stay, is a maintenance fork: your session is left untouched.
func runFork(loaded *config.Loaded, idFlag string, args []string, stay bool) {
	target := idFlag
	if target == "" && len(args) > 0 {
		target = args[0]
	}
	// Split an optional :<LT> suffix off the target.
	var atMainLT uint64
	if i := strings.LastIndex(target, ":"); i >= 0 {
		lt, err := strconv.ParseUint(target[i+1:], 10, 64)
		if err != nil {
			die("fork: bad :<LT> in %q (want <id>:<n>)", target)
		}
		atMainLT = lt
		target = target[:i]
	}

	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()

		bound := ""
		if r, err := acli.Resolve(ctx, ppid); err == nil && r.Found {
			bound = r.FigaroID
		}
		if target == "" {
			if bound == "" {
				die("fork: no aria bound to this shell (try: <id> or <id>:<LT>)")
			}
			target = bound
		}

		resp, err := acli.Fork(ctx, target, atMainLT)
		if err != nil {
			die("fork: %s", err)
		}

		// Rebind only when we forked our own bound aria (it just froze, so
		// the continuation is where "we" continue) and --stay wasn't given.
		rescoped := false
		if target == bound && !stay {
			acli.Unbind(ctx, ppid)
			if err := acli.Bind(ctx, ppid, resp.Continuation); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not bind shell to continuation: %s\n", err)
			} else {
				rescoped = true
			}
		}

		at := "head"
		if atMainLT > 0 {
			at = fmt.Sprintf("LT %d", atMainLT)
		}
		contNote := "(attend to continue)"
		if rescoped {
			contNote = "(this shell)"
		}
		fmt.Fprintf(os.Stderr,
			"forked %s at %s (now a frozen fork point)\n  continuation %s  %s\n  alternative  %s  (attend it to diverge)\n",
			resp.Parent, at, resp.Continuation, contNote, resp.Alternative)
		return nil
	})
}

func runAttendByID(loaded *config.Loaded, figaroID string) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()
		acli.Unbind(ctx, ppid)
		if err := acli.Bind(ctx, ppid, figaroID); err != nil {
			die("attend: %s", err)
		}
		fmt.Fprintf(os.Stderr, "attending %s\n", figaroID)
		return nil
	})
}

// runDetach unbinds this shell's PPID from its figaro.
func runDetach(loaded *config.Loaded) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()
		resp, err := acli.Resolve(ctx, ppid)
		if err != nil {
			die("resolve: %s", err)
		}
		if !resp.Found {
			fmt.Fprintln(os.Stderr, "no figaro bound to this shell")
			return nil
		}
		if err := acli.Unbind(ctx, ppid); err != nil {
			die("unbind: %s", err)
		}
		fmt.Fprintf(os.Stderr, "detached from %s\n", resp.FigaroID)
		return nil
	})
}
