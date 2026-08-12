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
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/term"
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

// The figtree field names `list` populates and its columns read.
const (
	fieldID     = "id"
	fieldOutfit = "outfit"
	fieldVer    = "ver"
	fieldFork   = "fork"
	fieldAge    = "age"
	fieldMsgs   = "msgs"
	fieldCtx    = "ctx"
	fieldCwd    = "cwd"
	fieldDetail = "detail"
	fieldKind   = "kind"
)

// listColumns is the table `list` prints at a given width: the full nine, a
// reduced six, or: for the global hierarchy, whose rows are anchors rather
// than conversations: the tree, its id and one detail line.
func listColumns(width int, global bool) []figtree.Column {
	switch {
	case global:
		return []figtree.Column{
			{Header: "ARIA"},
			{Header: "ID", Field: fieldID},
			{Header: "DETAIL", Field: fieldDetail},
		}
	case width < listFullWidth:
		return []figtree.Column{
			{Header: "ARIA"},
			{Header: "ID", Field: fieldID},
			{Header: "OUTFIT", Field: fieldOutfit, Max: 18},
			{Header: "AGE", Field: fieldAge},
			{Header: "MSGS", Field: fieldMsgs},
			{Header: "CTX", Field: fieldCtx},
		}
	default:
		return []figtree.Column{
			{Header: "ARIA"},
			{Header: "ID", Field: fieldID},
			{Header: "OUTFIT", Field: fieldOutfit},
			{Header: "VER", Field: fieldVer},
			{Header: "FORK", Field: fieldFork},
			{Header: "AGE", Field: fieldAge},
			{Header: "MSGS", Field: fieldMsgs},
			{Header: "CTX", Field: fieldCtx},
			{Header: "CWD", Field: fieldCwd},
		}
	}
}

func runList(loaded *config.Loaded, o lsOpts) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// --json: the prodev escape hatch: the whole store (incl. the null +
		// outfit anchors) as one JSON array. No scoping, no rendering.
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
		if r, rerr := resolveBinding(ctx, acli, shellPID); rerr == nil && r.Found {
			boundID = r.FigaroID
		}

		// ATTENDING A FORM. The flat aria table cannot show one: figaro.list
		// answers with figaros, so the form has no row, the ● has nothing to
		// land on, and the scope quietly degrades to home. Draw the genealogy
		// instead, which already knows how to render a form and a role, and
		// bound the same way the aria listing bounds itself: from the form's
		// parent, one level past the form.
		if !o.global && o.rootID == "" && !o.home && strings.HasPrefix(boundID, "@") {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			renderFormScope(resp.Figaros, boundID, o.limit)
			return nil
		}

		// Global view: the full null → outfit → conversation → branch tree.
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
			// explicit subtree: keep
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

		ppid := shellPID
		tree, roots := listForest(figs, rootID, ppid)
		rows := tree.Rows()

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
		width := listOutputWidth()
		summary := ""
		if width < listCompactWidth {
			summary = fmt.Sprintf("%d aria(s) · %d branch(es) · %d/%d%s · ● here ▸ running ○ idle",
				len(roots), branches, shown, total, hint)
		} else {
			summary = fmt.Sprintf("%d top-level aria(s), %d branch(es) · showing %d of %d%s        ●=here ▸=running ○=idle",
				len(roots), branches, shown, total, hint)
		}
		fmt.Fprintln(os.Stderr, truncateVisible(summary, width))
		fmt.Fprintln(os.Stderr)
		fmt.Fprint(os.Stdout, renderListRows(rows, width, false))
		if limit > 0 && total > limit {
			fmt.Fprintf(os.Stderr, "\n… %d more (-a for all, -n N for N)\n", total-limit)
		}
		return nil
	})
}

// listForest builds the rendered rows for the scoped fork forest, and the
// top-level roots it grouped them under. Pure: the caller has already
// fetched and scoped figs.
func listForest(figs []rpc.FigaroInfoResponse, rootID string, ppid int) (figtree.Tree, []rpc.FigaroInfoResponse) {
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
		// Roots are depth-0 conversations, or: when scoped to a subtree -
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
	marker := func(f rpc.FigaroInfoResponse) string {
		if slices.Contains(f.BoundPIDs, ppid) {
			return "●"
		}
		if f.State == "active" {
			return "▸"
		}
		return "○"
	}
	build := func(f rpc.FigaroInfoResponse) *figtree.Node {
		label := f.Mantra
		if label == "" {
			label = "aria " + f.ID
		}
		ctxStr := "-"
		if f.ContextTokens > 0 {
			ctxStr = formatCtxCell(f.ContextTokens)
			if !f.ContextExact {
				ctxStr = "~" + ctxStr
			}
		}
		// Branches are MARKED, not numbered. The forest records a fork point
		// as an LT (BranchedLT), but the coordinate `send/fork <id>:N` takes
		// is a TURN: printing the LT here and letting it read as a fork
		// argument is the exact class of trap this project exists to remove
		// (the old comment claimed the two were the same; they never were
		// after turn addressing, and are off by a whole exchange).
		//
		// Resolving LT -> turn needs the trunk's message log, which `list`
		// deliberately does not read: it would be one full log read per
		// branch on every listing. `figaro status <id>` does read it and
		// prints the exact `parent:turn` you can fork with.
		fork := "-"
		if len(f.Vector) > 1 && f.BranchedLT > 1 {
			fork = "yes"
		}
		return &figtree.Node{
			Marker: marker(f),
			Label:  truncRunes(label, 44),
			Fields: map[string]string{
				fieldID:     f.ID,
				fieldOutfit: dash(bornOf(f)),
				fieldVer:    dash(f.OutfitVer),
				fieldFork:   fork,
				fieldAge:    relAge(f.LastActive),
				fieldMsgs:   fmt.Sprintf("%d", f.MessageCount),
				fieldCtx:    ctxStr,
				fieldCwd:    shortCwd(f.Cwd),
			},
		}
	}
	var grow func(f rpc.FigaroInfoResponse) *figtree.Node
	grow = func(f rpc.FigaroInfoResponse) *figtree.Node {
		n := build(f)
		for _, c := range kids[vecKey(f.Vector)] {
			n.Children = append(n.Children, grow(c))
		}
		return n
	}
	tree := figtree.Tree{}
	for _, r := range roots {
		tree.Roots = append(tree.Roots, grow(r))
	}
	return tree, roots
}

// renderGlobal prints the full hierarchy: null → outfits → conversations →
// branches: by parent links. ● marks the attended aria, or, when detached,
// the live outfit (your implicit home).
// globalForest builds the rendered rows for the whole hierarchy, walked by
// the DRAWN edge (promote's override, else history). Pure, so its shape can
// be pinned without an angelus.
func globalForest(figs []rpc.FigaroInfoResponse, boundID string, ppid int) figtree.Tree {
	return forestFrom(figs, "", -1, boundID, ppid)
}

// forestFrom grows the same tree globalForest does, from an arbitrary root and
// to a bounded depth. rootID "" means the null genesis root; maxDepth < 0 means
// no limit, and 0 means the root alone.
//
// It exists so that attending a FORM can be shown in the tree that already
// knows how to draw forms, rather than in the flat aria table that cannot: the
// scoped `fig ls` used to fall back to the home listing, silently, because
// figaro.list returns figaros only and the form could not be placed. The
// result was byte-identical to attending nothing at all.
func forestFrom(figs []rpc.FigaroInfoResponse, rootID string, maxDepth int, boundID string, ppid int) figtree.Tree {
	byID := map[string]rpc.FigaroInfoResponse{}
	childrenOf := map[string][]string{}
	nullID := ""
	for _, f := range figs {
		byID[f.ID] = f
		childrenOf[drawnUnder(f)] = append(childrenOf[drawnUnder(f)], f.ID)
		if f.Kind == "null" {
			nullID = f.ID
		}
	}
	for p := range childrenOf {
		ids := childrenOf[p]
		sort.SliceStable(ids, func(i, j int) bool { return byID[ids[i]].LastActive > byID[ids[j]].LastActive })
	}
	liveOutfit := ""
	if boundID == "" {
		for _, f := range figs {
			if f.Kind == "outfit" && f.OutfitVer == "live" {
				liveOutfit = f.ID
				break
			}
		}
	}
	mark := func(f rpc.FigaroInfoResponse) string {
		if slices.Contains(f.BoundPIDs, ppid) || (f.ID != "" && f.ID == liveOutfit) {
			return "●"
		}
		if f.State == "active" {
			return "▸"
		}
		return "○"
	}
	var grow func(id string, depth int) *figtree.Node
	grow = func(id string, depth int) *figtree.Node {
		f := byID[id]
		var label, detail string
		switch f.Kind {
		case "null":
			label, detail = "null", "genesis root · ceremonial"
		case "outfit":
			ver := f.OutfitVer
			if ver == "" {
				ver = "?"
			}
			label, detail = "outfit "+dash(f.OutfitName)+"@"+ver, "ceremonial"
		case "form":
			label = "form " + f.ID
			if f.Name != "" {
				label = f.Name + " " + f.ID
			}
			detail = "unbound"
			if f.TargetAria != "" {
				detail = "role → " + f.TargetAria
			}
		default:
			label = f.Mantra
			if label == "" {
				label = "aria " + f.ID
			}
			detail = fmt.Sprintf("%d msgs", f.MessageCount)
		}
		n := &figtree.Node{
			Marker: mark(f),
			Label:  truncRunes(label, 46),
			Fields: map[string]string{
				fieldID:     f.ID,
				fieldDetail: detail,
				fieldMsgs:   strconv.Itoa(f.MessageCount),
				fieldKind:   f.Kind,
			},
		}
		if maxDepth < 0 || depth < maxDepth {
			for _, c := range childrenOf[id] {
				n.Children = append(n.Children, grow(c, depth+1))
			}
		}
		return n
	}
	tree := figtree.Tree{
		// Unbound forms wear the transcript selection's wash: one shared
		// token (bgFormRow), so the two surfaces can never drift apart.
		Backgrounds: []figtree.RowBackground{{Field: fieldKind, Value: "form", Seq: bgFormRow}},
	}
	root := rootID
	if root == "" {
		root = nullID
	}
	if _, ok := byID[root]; ok {
		tree.Roots = append(tree.Roots, grow(root, 0))
	}
	return tree
}

// drawnUnder is the row's place in the drawn tree: where a promote put it,
// else where its history did.
func drawnUnder(f rpc.FigaroInfoResponse) string {
	if f.Present != "" {
		return f.Present
	}
	return f.Parent
}

// bgFormRow is the row wash for unbound forms in `ls -g`: the SAME
// sequence the transcript pager uses for node selection (see
// transcript_selection.go); grey 236 was chosen there against every
// theme, and this surface inherits that decision rather than remaking it.
const bgFormRow = "\x1b[48;5;236m"

// renderFormScope draws the attended FORM in its lineage: its parent as the
// root, the form beneath it, and the form's own children one level further.
// The same shape `fig ls` gives an aria, in the tree that can hold a form.
func renderFormScope(figs []rpc.FigaroInfoResponse, formID string, limit int) {
	parent := ""
	label := formID
	for _, f := range figs {
		if f.ID == formID {
			// drawnUnder, not Parent: the scope has to root where the TREE
			// draws this row, or a promoted form would be scoped under an
			// ancestor the listing no longer puts it beneath.
			parent = drawnUnder(f)
			if f.Name != "" {
				label = f.Name + " " + formID
			}
			if f.TargetAria != "" {
				label += " → " + f.TargetAria
			}
			break
		}
	}
	// Rooting at the parent needs the parent to be one level up, so the form
	// sits at depth 1 and its children at depth 2. A form whose parent is not
	// in the listing roots at the form itself.
	root, depth := parent, 2
	if root == "" {
		root, depth = formID, 1
	}
	rows := forestFrom(figs, root, depth, formID, shellPID).Rows()
	total := len(rows)
	shown := total
	if limit > 0 && total > limit {
		rows = rows[:limit]
		shown = limit
	}
	width := listOutputWidth()
	summary := fmt.Sprintf("form %s · showing %d of %d        ●=here ▸=running ○=idle", label, shown, total)
	if width < listCompactWidth {
		summary = fmt.Sprintf("form %s · %d/%d · ● here ▸ running ○ idle", label, shown, total)
	}
	fmt.Fprintln(os.Stderr, truncateVisible(summary, width))
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stdout, renderListRows(rows, width, true))
	if limit > 0 && total > limit {
		fmt.Fprintf(os.Stderr, "\n… %d more (-a for all, -n N for N)\n", total-limit)
	}
}

func renderGlobal(figs []rpc.FigaroInfoResponse, boundID string, limit int) {
	rows := globalForest(figs, boundID, shellPID).Rows()
	total := len(rows)
	shown := total
	if limit > 0 && total > limit {
		rows = rows[:limit]
		shown = limit
	}
	hint := " · attending " + boundID
	if boundID == "" {
		hint = " · home (live outfit)"
	}
	width := listOutputWidth()
	summary := ""
	if width < listCompactWidth {
		summary = fmt.Sprintf("global · %d/%d%s · ● here ▸ running ○ idle", shown, total, hint)
	} else {
		summary = fmt.Sprintf("global · showing %d of %d%s        ●=here ▸=running ○=idle", shown, total, hint)
	}
	fmt.Fprintln(os.Stderr, truncateVisible(summary, width))
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stdout, renderListRows(rows, width, true))
	if limit > 0 && total > limit {
		fmt.Fprintf(os.Stderr, "\n… %d more (-a for all, -n N for N)\n", total-limit)
	}
}

const (
	listCompactWidth = 100
	listFullWidth    = 140
)

func listOutputWidth() int {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return 10000
	}
	return term.Width()
}

// renderListRows chooses a table only when it can fit without terminal
// wrapping. Narrow terminals instead get one clipped hierarchy line per aria.
func renderListRows(rows []figtree.Row, width int, global bool) string {
	if width <= 0 {
		width = 80
	}
	if width < listCompactWidth {
		var out strings.Builder
		for _, r := range rows {
			fmt.Fprintln(&out, compactListRow(r, width))
		}
		return out.String()
	}
	return figtree.RenderRows(rows, listColumns(width, global), width)
}

func compactListRow(r figtree.Row, width int) string {
	id := truncRunes(r.Field(fieldID), 8)
	parts := []string{id}
	if age := r.Field(fieldAge); age != "" && age != "-" {
		parts = append(parts, age)
	}
	if msgs := r.Field(fieldMsgs); msgs != "" {
		parts = append(parts, msgs+"msg")
	}
	suffix := strings.Join(parts, " ")
	labelWidth := width - term.VisibleLen(suffix) - 1
	if labelWidth < 8 {
		suffix = id
		labelWidth = width - term.VisibleLen(suffix) - 1
	}
	if labelWidth <= 0 {
		return truncateVisible(r.Cell()+" "+suffix, width)
	}
	return truncateVisible(r.Cell(), labelWidth) + " " + suffix
}

func truncateVisible(s string, width int) string {
	if term.VisibleLen(s) <= width {
		return s
	}
	return term.TruncateVisible(s, width)
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
// contains id (the trunk whose vector is the first component of id's vector) -
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
// bornOf names what an aria was born under: its outfit, or: for a
// form-born aria: the @form itself (the nearest unbound ancestor).
func bornOf(f rpc.FigaroInfoResponse) string {
	if f.OutfitName == "" && strings.HasPrefix(f.Parent, "@") {
		return f.Parent
	}
	return f.OutfitName
}

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
		dieUsage("usage: figaro kill [--id <trunk> | <trunk>] [--recursive]")
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

// runFork branches a conversation, imperatively and with no prompt (the
// prompt-bearing form lives in fork.go). A HEAD fork keeps the target's id
// on the continuation and mints ONE new aria, the alternative; the target
// is NOT frozen and stays live at the same id. An INTERIOR fork (<id>:<turn>)
// splits at that turn instead.
//
// spec is the target: "" (the shell-bound aria), `<id>`, or `<id>:<turn>`
// for an interior fork at that turn.
//
// Rescoping: when you fork your OWN bound aria, the shell stays on the
// continuation, which KEEPS the target's id, trunk and mantra: the aria
// is not frozen and nothing addressing it breaks. Forking any OTHER aria,
// or passing --stay, is a maintenance fork: your session is left untouched.
func runFork(loaded *config.Loaded, spec string, opts sendOpts) {
	stay, asJSON := opts.stay, opts.json
	// Split an optional :<turn> suffix off the target. Shared parser: fork and
	// send must not drift apart on what a coordinate means.
	target, at, perr := parseTarget(spec)
	if perr != nil {
		dieUsage("fork: %s", perr)
	}

	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx := context.Background()
		ppid := shellPID

		bound := ""
		if r, err := resolveBinding(ctx, acli, ppid); err == nil && r.Found {
			bound = r.FigaroID
		}
		if target == "" {
			if bound == "" {
				dieUsage("fork: no aria bound to this shell (try: <id> or <id>:<turn>)")
			}
			target = bound
		}

		resp, err := waitForFork(ctx, acli, target, at, opts.outfit)
		if err != nil {
			die("fork: %s", err)
		}

		// Rebinding is mostly ceremony now: a fork keeps the aria id, so the
		// continuation IS the aria this shell is already bound to. The Bind
		// still earns its keep: it clears any pending fork point on the
		// binding (atLT 0), and a cauterized fork (redirected to a fresh
		// conversation under an outfit or the root) can hand back a
		// continuation that really is somewhere else. Registry.Bind rebinds
		// in place, so no Unbind is needed first.
		rescoped := false
		if target == bound && !stay {
			if err := bindBinding(ctx, acli, ppid, resp.Continuation, 0); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not bind shell to continuation: %s\n", err)
			} else {
				rescoped = true
			}
		}

		if asJSON {
			// aria_id is the "new/current" aria after the fork -
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
				Turn         uint64 `json:"turn,omitempty"`
				Rescoped     bool   `json:"rescoped"`
				OwnerNote    string `json:"owner_note,omitempty"`
				Mode         string `json:"mode"`
			}{
				AriaID:       ariaID,
				Parent:       resp.Parent,
				Continuation: resp.Continuation,
				Alternative:  resp.Alternative,
				Turn:         at.turn,
				Rescoped:     rescoped,
				OwnerNote:    resp.OwnerNote,
				Mode:         "fork",
			})
			return nil
		}

		if resp.OwnerNote != "" {
			fmt.Fprintf(os.Stderr, "%s\n", resp.OwnerNote)
		}
		contNote := "(attend to continue)"
		if rescoped {
			contNote = "(this shell)"
		}
		fmt.Fprintf(os.Stderr,
			"forked %s at %s\n  continuation %s  %s\n  alternative  %s  (attend it to diverge)\n",
			resp.Parent, at, resp.Continuation, contNote, resp.Alternative)
		return nil
	})
}

// runNormalize forces the deferred topology work: every aria presented away
// from where its history lives absorbs that history, after which no delete
// can owe a boundary repair. Blocking on purpose.
func runNormalize(loaded *config.Loaded, segments bool) {
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		resp, err := acli.Normalize(ctx, segments)
		if err != nil {
			die("normalize: %s", err)
		}
		if resp.Unsupported {
			die("normalize: this figaro has no trunk capability, so its hierarchy already\n" +
				"  follows fork history and there is nothing to normalize.")
		}
		switch resp.Detached {
		case 0:
			fmt.Fprintln(os.Stderr, "normalize: already normalized, nothing to absorb")
		case 1:
			fmt.Fprintln(os.Stderr, "normalize: 1 aria now owns its history outright")
		default:
			fmt.Fprintf(os.Stderr, "normalize: %d arias now own their history outright\n", resp.Detached)
		}
		return nil
	})
}

// runPromote raises an aria in the PRESENTATION hierarchy: it takes its
// parent's place in the tree fig ls draws, and the parent comes to sit under
// it. Nothing moves on disk and no history changes: the aria still reads
// exactly the turns it did before, so this is instant regardless of how long
// the conversation is. Needs the trunk capability.
func runPromote(loaded *config.Loaded, idFlag string, args []string) {
	target := idFlag
	if target == "" && len(args) > 0 {
		target = args[0]
	}
	levels := 1
	if len(args) > 1 {
		n, err := strconv.Atoi(args[len(args)-1])
		if err != nil || n < 1 {
			dieUsage("promote: bad level count %q (want a positive integer)", args[len(args)-1])
		}
		levels = n
	}
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if target == "" {
			if r, err := resolveBinding(ctx, acli, shellPID); err == nil && r.Found {
				target = r.FigaroID
			}
		}
		if target == "" {
			dieUsage("promote: no aria bound to this shell (try: promote <id> [levels])")
		}
		resp, err := acli.Promote(ctx, target, levels)
		if err != nil {
			die("promote: %s", err)
		}
		if resp.Unsupported {
			die("promote: this figaro is built without the trunk capability, so there is\n" +
				"  no hierarchy to promote within. Aria nesting follows fork history alone.\n" +
				"  Set `trunks = true` in config.toml and restart the daemon to enable it.")
		}
		if resp.AtStump {
			die("promote: nothing above %s but an outfit, and only conversations nest;\n"+
				"  edit the outfit instead, or promote something under this one", target)
		}
		fmt.Fprintf(os.Stderr, "promoted %s by %d level(s): `figaro ls` now draws it there\n", target, resp.Climbed)
		return nil
	})
}

// runAttend binds this shell to a target spec: <id>, <id>:<LT>, or :<LT>.
// A bare id pins the trunk's leaf; an LT pins a pending fork-point (the next
// prompt forks there and moves to the new branch). The :<LT> form re-pins the
// already-bound aria.
func runAttend(loaded *config.Loaded, spec string) {
	// A statically-attended shell (FIGARO_ARIA, an aria's own bash
	// tool) has an identity, not a binding: there is nothing to move.
	if id := envAriaID(); id != "" {
		die("attend: this shell is statically attended to %s (FIGARO_ARIA) and cannot be re-attended.\n"+
			"  figaro send --id <id> -- <prompt>   talk to another aria\n"+
			"  figaro show --id <id>               read another aria", id)
	}
	if bindingDisabled() {
		die("attend: binding disabled (--no-bind, FIGARO_NO_BIND, or non-interactive shell); this command has no effect here")
	}
	// "null" is home: drop this shell's binding (the angelus pid→aria map),
	// echoing the kindNull genesis root that sits above every outfit. New
	// conversations then default to the live outfit. `null` is a required
	// literal; there is no `detach`. `~` is kept as a legacy alias so old
	// muscle memory still works (it must be quoted in the shell).
	if spec == "null" || spec == "~" {
		runUnattend(loaded)
		return
	}
	trunk, at, err := parseTarget(spec)
	if err != nil {
		die("attend: %s", err)
	}
	WithAngelus(loaded, func(acli *angelus.Client) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ppid := shellPID
		if trunk == "" {
			r, rerr := resolveBinding(ctx, acli, ppid)
			if rerr != nil || !r.Found {
				die("attend: :<turn> needs an already-bound aria (use attend <id>:<turn>)")
			}
			trunk = r.FigaroID
		}
		// A binding anchor is an LT, so a named TURN is resolved here; a
		// named LT is already the thing bindBinding wants.
		atMainLT := at.lt
		if at.turn > 0 {
			lt, rerr := resolveTurn(ctx, acli, trunk, at.turn)
			if rerr != nil {
				die("attend: %s", rerr)
			}
			atMainLT = lt
		}
		if err := bindBinding(ctx, acli, ppid, trunk, atMainLT); err != nil {
			// A cauterized anchor (null/outfit) can't be attended: nudge.
			if r, e := acli.ListGlobal(ctx); e == nil {
				for _, f := range r.Figaros {
					if f.ID == trunk && (f.Kind == "null" || f.Kind == "outfit") {
						die("%s is a %s: a closed anchor, not a conversation; it can't be attended.\n"+
							"  figaro attend null  go home (unbind; new conversations use the live outfit)\n"+
							"  figaro ls -h        lists top-level conversations (use -a or -n N to show all or N most recent in scope)\n"+
							"  figaro ls -g        show the full hierarchy (null + outfits + conversations)", trunk, f.Kind)
					}
				}
			}
			die("attend: %s", err)
		}
		if !at.isHead() {
			fmt.Fprintf(os.Stderr, "attending %s at %s (next prompt forks there)\n", trunk, at)
		} else {
			fmt.Fprintf(os.Stderr, "attending %s\n", trunk)
		}
		return nil
	})
}
