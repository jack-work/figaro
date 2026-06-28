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
	"github.com/jack-work/figaro/internal/rpc"
)

// runList prints the conversation forest (live, dormant, and frozen fork
// points), ordered as a depth-first walk of the fork tree so each trunk's
// lineage reads top-to-bottom. The leading VECTOR column (0, 0.0, 0.1, …)
// shows fork depth + branch; MANTRA is the thread's essence. With jsonOut
// the raw entries are emitted instead.
func runList(loaded *config.Loaded, jsonOut bool, limit int) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp, err := acli.List(ctx)
		if err != nil {
			die("list: %s", err)
		}
		figs := resp.Figaros

		if jsonOut {
			sort.SliceStable(figs, func(i, j int) bool { return vectorLess(figs[i].Vector, figs[j].Vector) })
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(figs); err != nil {
				die("list --json: %s", err)
			}
			return nil
		}

		// Build the fork forest: index by vector, group children, collect
		// roots (depth-0 conversations). Trees float up by their most-recent
		// member; within a tree, children sort by branch order (vector).
		byVec := map[string]rpc.FigaroInfoResponse{}
		kids := map[string][]rpc.FigaroInfoResponse{}
		var roots []rpc.FigaroInfoResponse
		for _, f := range figs {
			if len(f.Vector) == 0 {
				continue
			}
			byVec[vecKey(f.Vector)] = f
			if len(f.Vector) == 1 {
				roots = append(roots, f)
			} else {
				pk := vecKey(f.Vector[:len(f.Vector)-1])
				kids[pk] = append(kids[pk], f)
			}
		}
		lastComp := func(v []int) int { return v[len(v)-1] }
		for k := range kids {
			ks := kids[k]
			sort.Slice(ks, func(i, j int) bool { return lastComp(ks[i].Vector) < lastComp(ks[j].Vector) })
		}
		var subtreeRecency func(f rpc.FigaroInfoResponse) int64
		subtreeRecency = func(f rpc.FigaroInfoResponse) int64 {
			best := f.LastActive
			for _, c := range kids[vecKey(f.Vector)] {
				if r := subtreeRecency(c); r > best {
					best = r
				}
			}
			return best
		}
		sort.SliceStable(roots, func(i, j int) bool {
			return subtreeRecency(roots[i]) > subtreeRecency(roots[j])
		})

		// Flatten to rendered rows: tree glyphs in an ARIA cell.
		type row struct {
			aria, id, fork, age, msgs, ctx, cwd string
		}
		var rows []row
		ppid := os.Getppid()
		marker := func(f rpc.FigaroInfoResponse) string {
			if slices.Contains(f.BoundPIDs, ppid) {
				return "●"
			}
			if f.State == "active" {
				return "▸"
			}
			return "○"
		}
		var emit func(f rpc.FigaroInfoResponse, prefix string, isLast, isRoot bool)
		emit = func(f rpc.FigaroInfoResponse, prefix string, isLast, isRoot bool) {
			glyph := ""
			if !isRoot {
				glyph = prefix + "├─"
				if isLast {
					glyph = prefix + "└─"
				}
			}
			label := f.Mantra
			if label == "" {
				label = "aria " + f.ID
			}
			ctxStr := "-"
			if f.ContextTokens > 0 {
				ctxStr = fmt.Sprintf("%dk", f.ContextTokens/1000)
				if !f.ContextExact {
					ctxStr = "~" + ctxStr
				}
			}
			// Branches show the LT they were forked AT (the last shared LT,
			// = what `send/fork :N` reproduces). BranchedLT is the first own
			// LT, so the fork point is BranchedLT-1. Roots are top-level.
			fork := "-"
			if len(f.Vector) > 1 && f.BranchedLT > 1 {
				fork = fmt.Sprintf("@%d", f.BranchedLT-1)
			}
			rows = append(rows, row{
				aria: glyph + marker(f) + " " + truncRunes(label, 44),
				id:   f.ID, fork: fork, age: relAge(f.LastActive),
				msgs: fmt.Sprintf("%d", f.MessageCount), ctx: ctxStr, cwd: shortCwd(f.Cwd),
			})
			cp := prefix
			if !isRoot {
				if isLast {
					cp += "  "
				} else {
					cp += "│ "
				}
			}
			ck := kids[vecKey(f.Vector)]
			for i, c := range ck {
				emit(c, cp, i == len(ck)-1, false)
			}
		}
		for _, r := range roots {
			emit(r, "", true, true)
		}

		total := len(rows)
		shown := total
		if limit > 0 && total > limit {
			rows = rows[:limit]
			shown = limit
		}

		branches := 0
		for _, f := range figs {
			if len(f.Vector) > 1 {
				branches++
			}
		}
		fmt.Fprintf(os.Stderr, "%d trunk(s), %d branch(es) · showing %d of %d        ●=this shell  ▸=running  ○=idle\n\n",
			len(roots), branches, shown, total)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "ARIA\tID\tFORK\tAGE\tMSGS\tCTX\tCWD\n")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.aria, r.id, r.fork, r.age, r.msgs, r.ctx, r.cwd)
		}
		w.Flush()
		if limit > 0 && total > limit {
			fmt.Fprintf(os.Stderr, "\n… %d more (--all to show every trunk)\n", total-limit)
		}
		return nil
	})
}

// vecKey joins a vector into a stable map key (e.g. [0,1] -> "0.1").
func vecKey(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// truncRunes shortens s to at most n runes, appending ".." when cut.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-2]) + ".."
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

func runKill(loaded *config.Loaded, idFlag string, args []string, recursive bool) {
	ariaID := idFlag
	if ariaID == "" && len(args) > 0 {
		ariaID = args[0]
	}
	if ariaID == "" {
		die("usage: figaro kill [--id <trunk> | <trunk>] [--recursive]")
	}
	runKillByID(loaded, ariaID, recursive)
}

func runKillByID(loaded *config.Loaded, figaroID string, recursive bool) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := acli.Kill(ctx, figaroID, recursive); err != nil {
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
