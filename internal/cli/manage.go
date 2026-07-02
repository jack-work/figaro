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

// lsOpts captures the parsed `ls` flags. Scope: home/global/subtree(rootID);
// cap: limit (0 = all). See the `list` command for the flag surface.
type lsOpts struct {
	jsonOut bool
	home    bool
	global  bool
	limit   int
	rootID  string
}

func runList(loaded *config.Loaded, o lsOpts) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// --json: the prodev escape hatch — the whole store (incl. the null +
		// loadout anchors) as one JSON array. No scoping, no rendering.
		if o.jsonOut {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			figs := resp.Figaros
			sort.SliceStable(figs, func(i, j int) bool { return vectorLess(figs[i].Vector, figs[j].Vector) })
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(figs); err != nil {
				die("list --json: %s", err)
			}
			return nil
		}

		boundID := ""
		if r, rerr := resolveBinding(ctx, acli, os.Getppid()); rerr == nil && r.Found {
			boundID = r.FigaroID
		}

		// Global view: the full null → loadout → conversation → branch tree.
		if o.global {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			renderGlobal(resp.Figaros, boundID, o.limit)
			return nil
		}

		limit := o.limit
		resp, err := acli.List(ctx)
		if err != nil {
			die("list: %s", err)
		}
		figs := resp.Figaros

		// Scope. `<id>` → that subtree. `-h`/--home → the whole forest (● stays
		// on you). Default: attended scopes to your conversation's tree;
		// detached shows the whole forest. "/" forces the whole forest.
		rootID := o.rootID
		switch {
		case rootID == "/":
			rootID = ""
		case rootID != "":
			// explicit subtree — keep
		case o.home:
			rootID = ""
		case boundID != "":
			rootID = topLevelAncestor(figs, boundID)
		}

		// Subtree scope: keep only the named trunk and everything forked
		// below it (vectors with its vector as a prefix).
		if rootID != "" {
			var rootVec []int
			for i := range figs {
				if figs[i].ID == rootID {
					rootVec = figs[i].Vector
					break
				}
			}
			if rootVec == nil {
				die("no aria %q (try: figaro list)", rootID)
			}
			kept := figs[:0:0]
			for _, f := range figs {
				if hasVecPrefix(f.Vector, rootVec) {
					kept = append(kept, f)
				}
			}
			figs = kept
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
			// Roots are depth-0 conversations, or — when scoped to a subtree —
			// the named trunk itself; everything else nests under its parent.
			isRoot := len(f.Vector) == 1
			if rootID != "" {
				isRoot = f.ID == rootID
			}
			if isRoot {
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
			aria, id, loadout, ver, fork, age, msgs, ctx, cwd string
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
				id:   f.ID, loadout: dash(f.LoadoutName), ver: dash(f.LoadoutVer),
				fork: fork, age: relAge(f.LastActive),
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
		hint := " · home"
		if boundID != "" {
			if o.home {
				hint = " · home (attending " + boundID + ")"
			} else {
				hint = " · attending " + boundID
			}
		}
		fmt.Fprintf(os.Stderr, "%d top-level aria(s), %d branch(es) · showing %d of %d%s        ●=here ▸=running ○=idle\n\n",
			len(roots), branches, shown, total, hint)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintf(w, "ARIA\tID\tLOADOUT\tVER\tFORK\tAGE\tMSGS\tCTX\tCWD\n")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.aria, r.id, r.loadout, r.ver, r.fork, r.age, r.msgs, r.ctx, r.cwd)
		}
		w.Flush()
		if limit > 0 && total > limit {
			fmt.Fprintf(os.Stderr, "\n… %d more (-a for all, -n N for N)\n", total-limit)
		}
		return nil
	})
}

// renderGlobal prints the full hierarchy — null → loadouts → conversations →
// branches — by parent links. ● marks the attended aria, or, when detached,
// the live loadout (your implicit home).
func renderGlobal(figs []rpc.FigaroInfoResponse, boundID string, limit int) {
	byID := map[string]rpc.FigaroInfoResponse{}
	childrenOf := map[string][]string{}
	nullID := ""
	for _, f := range figs {
		byID[f.ID] = f
		childrenOf[f.Parent] = append(childrenOf[f.Parent], f.ID)
		if f.Kind == "null" {
			nullID = f.ID
		}
	}
	for p := range childrenOf {
		ids := childrenOf[p]
		sort.SliceStable(ids, func(i, j int) bool { return byID[ids[i]].LastActive > byID[ids[j]].LastActive })
	}
	liveLoadout := ""
	if boundID == "" {
		for _, f := range figs {
			if f.Kind == "loadout" && f.LoadoutVer == "live" {
				liveLoadout = f.ID
				break
			}
		}
	}
	ppid := os.Getppid()
	mark := func(f rpc.FigaroInfoResponse) string {
		if slices.Contains(f.BoundPIDs, ppid) || (f.ID != "" && f.ID == liveLoadout) {
			return "●"
		}
		if f.State == "active" {
			return "▸"
		}
		return "○"
	}
	type row struct{ aria, id, detail string }
	var rows []row
	var emit func(id, prefix string, isLast, isRoot bool)
	emit = func(id, prefix string, isLast, isRoot bool) {
		f := byID[id]
		glyph := ""
		if !isRoot {
			glyph = prefix + "├─"
			if isLast {
				glyph = prefix + "└─"
			}
		}
		var label, detail string
		switch f.Kind {
		case "null":
			label, detail = "null", "genesis root · ceremonial"
		case "loadout":
			ver := f.LoadoutVer
			if ver == "" {
				ver = "?"
			}
			label, detail = "loadout "+dash(f.LoadoutName)+"@"+ver, "ceremonial"
		default:
			label = f.Mantra
			if label == "" {
				label = "aria " + f.ID
			}
			detail = fmt.Sprintf("%d msgs", f.MessageCount)
		}
		rows = append(rows, row{aria: glyph + mark(f) + " " + truncRunes(label, 46), id: f.ID, detail: detail})
		cp := prefix
		if !isRoot {
			if isLast {
				cp += "  "
			} else {
				cp += "│ "
			}
		}
		ck := childrenOf[id]
		for i, c := range ck {
			emit(c, cp, i == len(ck)-1, false)
		}
	}
	if nullID != "" {
		emit(nullID, "", true, true)
	}
	total := len(rows)
	shown := total
	if limit > 0 && total > limit {
		rows = rows[:limit]
		shown = limit
	}
	hint := " · attending " + boundID
	if boundID == "" {
		hint = " · home (live loadout)"
	}
	fmt.Fprintf(os.Stderr, "global · showing %d of %d%s        ●=here ▸=running ○=idle\n\n", shown, total, hint)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "ARIA\tID\tDETAIL\n")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.aria, r.id, r.detail)
	}
	w.Flush()
	if limit > 0 && total > limit {
		fmt.Fprintf(os.Stderr, "\n… %d more (-a for all, -n N for N)\n", total-limit)
	}
}

// vecKey joins a vector into a stable map key (e.g. [0,1] -> "0.1").
func vecKey(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// topLevelAncestor returns the id of the top-level conversation trunk that
// contains id (the trunk whose vector is the first component of id's vector) —
// i.e. the root of id's whole fork tree. Falls back to id if not found.
func topLevelAncestor(figs []rpc.FigaroInfoResponse, id string) string {
	var vec []int
	for _, f := range figs {
		if f.ID == id {
			vec = f.Vector
			break
		}
	}
	if len(vec) == 0 {
		return id
	}
	for _, f := range figs {
		if len(f.Vector) == 1 && f.Vector[0] == vec[0] {
			return f.ID
		}
	}
	return id
}

// hasVecPrefix reports whether v lies at or below prefix in the fork forest
// (prefix is an ancestor-or-self of v).
func hasVecPrefix(v, prefix []int) bool {
	if len(v) < len(prefix) {
		return false
	}
	for i := range prefix {
		if v[i] != prefix[i] {
			return false
		}
	}
	return true
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
func runFork(loaded *config.Loaded, idFlag string, args []string, stay, asJSON bool) {
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
		if r, err := resolveBinding(ctx, acli, ppid); err == nil && r.Found {
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
			unbindBinding(ctx, acli, ppid)
			if err := bindBinding(ctx, acli, ppid, resp.Continuation, 0); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not bind shell to continuation: %s\n", err)
			} else {
				rescoped = true
			}
		}

		if asJSON {
			// aria_id is the "new/current" aria after the fork —
			// the continuation when we rescoped (this shell moved),
			// otherwise the alternative (what the caller usually cares
			// about when scripting: the fresh empty branch).
			ariaID := resp.Alternative
			if rescoped {
				ariaID = resp.Continuation
			}
			enc := json.NewEncoder(os.Stdout)
			_ = enc.Encode(struct {
				AriaID       string `json:"aria_id"`
				Parent       string `json:"parent"`
				Continuation string `json:"continuation"`
				Alternative  string `json:"alternative"`
				AtLT         uint64 `json:"at_lt,omitempty"`
				Rescoped     bool   `json:"rescoped"`
				OwnerNote    string `json:"owner_note,omitempty"`
				Mode         string `json:"mode"`
			}{
				AriaID:       ariaID,
				Parent:       resp.Parent,
				Continuation: resp.Continuation,
				Alternative:  resp.Alternative,
				AtLT:         atMainLT,
				Rescoped:     rescoped,
				OwnerNote:    resp.OwnerNote,
				Mode:         "fork",
			})
			return nil
		}

		at := "head"
		if atMainLT > 0 {
			at = fmt.Sprintf("LT %d", atMainLT)
		}
		if resp.OwnerNote != "" {
			fmt.Fprintf(os.Stderr, "%s\n", resp.OwnerNote)
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

// runPromote climbs a conversation trunk up N stump-bounded levels — it
// becomes the canonical line through its ancestors, absorbing each parent
// trunk's run. Pure relabeling: no data moves, ids are stable, your binding
// is untouched.
func runPromote(loaded *config.Loaded, idFlag string, args []string) {
	target := idFlag
	if target == "" && len(args) > 0 {
		target = args[0]
	}
	levels := 1
	if len(args) > 1 {
		n, err := strconv.Atoi(args[len(args)-1])
		if err != nil || n < 1 {
			die("promote: bad level count %q (want a positive integer)", args[len(args)-1])
		}
		levels = n
	}
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if target == "" {
			if r, err := resolveBinding(ctx, acli, os.Getppid()); err == nil && r.Found {
				target = r.FigaroID
			}
		}
		if target == "" {
			die("promote: no aria bound to this shell (try: promote <id> [levels])")
		}
		resp, err := acli.Promote(ctx, target, levels)
		if err != nil {
			die("promote: %s", err)
		}
		if resp.AtStump {
			die("promote: %s is rooted at a loadout — cannot promote into a loadout; make or edit a loadout instead", target)
		}
		fmt.Fprintf(os.Stderr, "promoted %s by %d level(s) — it is now the canonical line through its ancestors\n", target, resp.Climbed)
		return nil
	})
}

// runAttend binds this shell to a target spec: <id>, <id>:<LT>, or :<LT>.
// A bare id pins the trunk's leaf; an LT pins a pending fork-point (the next
// prompt forks there and moves to the new branch). The :<LT> form re-pins the
// already-bound aria.
func runAttend(loaded *config.Loaded, spec string) {
	if bindingDisabled() {
		die("attend: binding disabled (--no-bind, FIGARO_NO_BIND, or non-interactive shell); this command has no effect here")
	}
	// "~" is home: drop this shell's binding (the angelus pid→aria map). New
	// conversations then default to the live loadout. `~` is a required literal;
	// there is no `detach`.
	if spec == "~" {
		WithAngelus(loaded, func(acli *angelus.Client) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = unbindBinding(ctx, acli, os.Getppid())
			fmt.Fprintln(os.Stderr, "home — unbound; new conversations will use the live loadout")
			return nil
		})
		return
	}
	trunk, atMainLT, hasLT, err := parseSendTarget(spec)
	if err != nil {
		die("attend: %s", err)
	}
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := os.Getppid()
		if trunk == "" {
			r, rerr := resolveBinding(ctx, acli, ppid)
			if rerr != nil || !r.Found {
				die("attend: :<LT> needs an already-bound aria (use attend <id>:<LT>)")
			}
			trunk = r.FigaroID
		}
		if err := bindBinding(ctx, acli, ppid, trunk, atMainLT); err != nil {
			// A cauterized anchor (null/loadout) can't be attended — nudge.
			if r, e := acli.ListGlobal(ctx); e == nil {
				for _, f := range r.Figaros {
					if f.ID == trunk && (f.Kind == "null" || f.Kind == "loadout") {
						die("%s is a %s — a closed anchor, not a conversation; it can't be attended.\n"+
							"  figaro attend ~     go home (unbind; new conversations use the live loadout)\n"+
							"  figaro ls -h        lists top-level conversations (use -a or -n N to show all or N most recent in scope)\n"+
							"  figaro ls -g        show the full hierarchy (null + loadouts + conversations)", trunk, f.Kind)
					}
				}
			}
			die("attend: %s", err)
		}
		if hasLT {
			fmt.Fprintf(os.Stderr, "attending %s at LT %d (next prompt forks there)\n", trunk, atMainLT)
		} else {
			fmt.Fprintf(os.Stderr, "attending %s\n", trunk)
		}
		return nil
	})
}
