package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/sdk"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cli/figtree"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/mark"
	"github.com/jack-work/figaro/internal/term"
)

type lsOpts struct {
	jsonOut bool
	home    bool
	global  bool
	limit   int
	rootID  string
}

// forceHome is what `lsh` adds; the typed flags still adjudicate each other.
func listOptsFrom(ctx *cmdkit.RunContext, forceHome bool) lsOpts {
	o := lsOpts{
		jsonOut: ctx.BoolFlag("json"),
		home:    forceHome || ctx.BoolFlag("home"),
		global:  ctx.BoolFlag("global"),
		limit:   10,
	}
	if len(ctx.Args) > 0 {
		o.rootID = ctx.Args[0]
	}
	hasN := ctx.Flag("limit") != ""
	if o.jsonOut && (ctx.BoolFlag("home") || o.global || ctx.BoolFlag("all") || hasN || o.rootID != "") {
		die("ls --json is the global escape hatch and takes no other flags")
	}
	if ctx.BoolFlag("all") && hasN {
		die("ls: -a/--all and -n are mutually exclusive")
	}
	if ctx.BoolFlag("home") && o.global {
		die("ls: -H/--home and -g/--global are mutually exclusive")
	}
	if ctx.BoolFlag("all") {
		o.limit = 0
	} else if hasN {
		if n, err := strconv.Atoi(ctx.Flag("limit")); err == nil && n > 0 {
			o.limit = n
		}
	}
	return o
}

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
	defer mark.Span("ls", "json", o.jsonOut)()
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if o.jsonOut {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			figs := resp.Figaros
			sort.SliceStable(figs, func(i, j int) bool { return vectorLess(figs[i].Vector, figs[j].Vector) })
			enc := json.NewEncoder(stdout)
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

		// An attended form has no row in the flat table; draw its genealogy
		// instead, bounded from the form's parent.
		if !o.global && o.rootID == "" && !o.home && strings.HasPrefix(boundID, "@") {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			figs, hidden := dropExpired(resp.Figaros, time.Now().UnixMilli())
			renderFormScope(figs, boundID, o.limit)
			printExpiredNote(hidden)
			return nil
		}

		if o.global {
			resp, err := acli.ListGlobal(ctx)
			if err != nil {
				die("list: %s", err)
			}
			figs, hidden := dropExpired(resp.Figaros, time.Now().UnixMilli())
			renderGlobal(figs, boundID, o.limit)
			printExpiredNote(hidden)
			return nil
		}

		limit := o.limit
		resp, err := acli.List(ctx)
		if err != nil {
			die("list: %s", err)
		}
		figs, hidden := dropExpired(resp.Figaros, time.Now().UnixMilli())

		rootID, note, serr := lsScope(figs, boundID, o.rootID, o.home)
		if serr != nil {
			die("%s", serr)
		}
		if rootID != "" {
			scoped, ok := scopeSubtree(figs, rootID)
			if !ok {
				die("no aria %q (try: figaro list)", rootID)
			}
			figs = scoped
		}

		ppid := shellPID
		tree, roots := listTree(figs, rootID, ppid)
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
		fmt.Fprintln(stderrw, truncateVisible(summary, width))
		fmt.Fprintln(stderrw)
		printListRows(stdout, rows, width, false)
		if limit > 0 && total > limit {
			fmt.Fprintf(stderrw, "\n… %d more (-a for all, -n N for N)\n", total-limit)
		}
		if note != "" {
			fmt.Fprintf(stderrw, "\n%s\n", note)
		}
		printExpiredNote(hidden)
		return nil
	})
}

func printExpiredNote(n int) {
	if n <= 0 {
		return
	}
	fmt.Fprintf(stderrw, "\n%d expired, hidden until the daemon's sweep takes them (figaro doctor ttl)\n", n)
}

func listTree(figs []rpc.FigaroInfoResponse, rootID string, ppid int) (figtree.Tree, []rpc.FigaroInfoResponse) {
	// Trees float up by their most-recent member; within a tree, children sort
	// by vector.
	byVec := map[string]rpc.FigaroInfoResponse{}
	kids := map[string][]rpc.FigaroInfoResponse{}
	var roots []rpc.FigaroInfoResponse
	for _, f := range figs {
		if len(f.Vector) == 0 {
			continue
		}
		byVec[vecKey(f.Vector)] = f
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
		// Branches are marked, not numbered: the tree records a fork point as
		// an LT, but `send/fork <id>:N` takes a turn.
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

// globalTree walks the drawn edge: promote's override, else history.
func globalTree(figs []rpc.FigaroInfoResponse, boundID string, ppid int) figtree.Tree {
	return treeFrom(figs, "", -1, boundID, ppid)
}

// rootID "" means the null root; maxDepth < 0 means no limit, 0 the root alone.
func treeFrom(figs []rpc.FigaroInfoResponse, rootID string, maxDepth int, boundID string, ppid int) figtree.Tree {
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

// drawnUnder is where a promote put the row, else where its history did.
func drawnUnder(f rpc.FigaroInfoResponse) string {
	if f.Present != "" {
		return f.Present
	}
	return f.Parent
}

// bgFormRow is the transcript pager's selection wash, shared so the two
// surfaces cannot drift.
const bgFormRow = "\x1b[48;5;236m"

func renderFormScope(figs []rpc.FigaroInfoResponse, formID string, limit int) {
	parent := ""
	label := formID
	for _, f := range figs {
		if f.ID == formID {
			// drawnUnder, not Parent: a promoted form must scope under
			// the row the tree draws it beneath.
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
	root, depth := parent, 2
	if root == "" {
		root, depth = formID, 1
	}
	rows := treeFrom(figs, root, depth, formID, shellPID).Rows()
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
	fmt.Fprintln(stderrw, truncateVisible(summary, width))
	fmt.Fprintln(stderrw)
	printListRows(stdout, rows, width, true)
	if limit > 0 && total > limit {
		fmt.Fprintf(stderrw, "\n… %d more (-a for all, -n N for N)\n", total-limit)
	}
}

func renderGlobal(figs []rpc.FigaroInfoResponse, boundID string, limit int) {
	rows := globalTree(figs, boundID, shellPID).Rows()
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
	fmt.Fprintln(stderrw, truncateVisible(summary, width))
	fmt.Fprintln(stderrw)
	printListRows(stdout, rows, width, true)
	if limit > 0 && total > limit {
		fmt.Fprintf(stderrw, "\n… %d more (-a for all, -n N for N)\n", total-limit)
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

// rowSink takes a line with the id it is about: an id is data the verb hands
// over, never read back out of the rendering.
type rowSink interface {
	WriteRow(line, id string)
}

func printListRows(w io.Writer, rows []figtree.Row, width int, global bool) {
	text := renderListRows(rows, width, global)
	sink, ok := w.(rowSink)
	if !ok {
		fmt.Fprint(w, text)
		return
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	off := len(lines) - len(rows)
	for i, line := range lines {
		id := ""
		if i >= off && off >= 0 {
			id = rows[i-off].Field(fieldID)
		}
		sink.WriteRow(line, id)
	}
}

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

func vecKey(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

// An empty root means the home view; arg is an aria id, "/" for home, or a "^"-climb.
func lsScope(figs []rpc.FigaroInfoResponse, boundID, arg string, home bool) (rootID, note string, err error) {
	if strings.HasPrefix(arg, "^") {
		n, ok := parseClimb(arg)
		if !ok {
			return "", "", fmt.Errorf("ls %s: ^ climbs layers up (^, ^^, ^2)", arg)
		}
		if boundID == "" || strings.HasPrefix(boundID, "@") {
			return "", "", fmt.Errorf("ls %s: climbs from the attended aria; this shell is attended to none", arg)
		}
		return climbFrom(figs, boundID, n)
	}
	switch {
	case arg == "/":
		return "", "", nil
	case arg != "":
		return arg, "", nil
	case home, boundID == "", strings.HasPrefix(boundID, "@"):
		return "", "", nil
	}
	return boundID, "", nil
}

func parseClimb(arg string) (int, bool) {
	rest := strings.TrimLeft(arg, "^")
	carets := len(arg) - len(rest)
	if carets == 0 {
		return 0, false
	}
	if rest == "" {
		return carets, true
	}
	if carets > 1 {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func climbFrom(figs []rpc.FigaroInfoResponse, boundID string, n int) (string, string, error) {
	var vec []int
	for _, f := range figs {
		if f.ID == boundID {
			vec = f.Vector
			break
		}
	}
	if len(vec) == 0 {
		return "", "", fmt.Errorf("ls: the attended aria %s is not in this listing", boundID)
	}
	depth := len(vec) - n
	clamped := depth < 1
	if clamped {
		depth = 1
	}
	for d := depth; d >= 1; d-- {
		for _, f := range figs {
			if len(f.Vector) == d && hasVecPrefix(vec[:d], f.Vector) {
				if clamped {
					return f.ID, fmt.Sprintf("^%d passes the root: scoped to the top-level aria %s", n, f.ID), nil
				}
				return f.ID, "", nil
			}
		}
	}
	return "", "", fmt.Errorf("ls: no ancestor of %s is in this listing", boundID)
}

func scopeSubtree(figs []rpc.FigaroInfoResponse, rootID string) ([]rpc.FigaroInfoResponse, bool) {
	var rootVec []int
	for i := range figs {
		if figs[i].ID == rootID {
			rootVec = figs[i].Vector
			break
		}
	}
	if rootVec == nil {
		return nil, false
	}
	kept := figs[:0:0]
	for _, f := range figs {
		if hasVecPrefix(f.Vector, rootVec) {
			kept = append(kept, f)
		}
	}
	return kept, true
}

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

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-2]) + ".."
}

func vectorLess(a, b []int) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) != 0
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// bornOf is the outfit, or for a form-born aria the nearest unbound ancestor.
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
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := acli.Kill(ctx, figaroID, recursive); err != nil {
			die("kill: %s", err)
		}
		fmt.Fprintf(stderrw, "killed %s\n", figaroID)
		return nil
	})
}

// A head fork keeps the target's id on the continuation and mints one
// alternative; an interior fork (<id>:<turn>) splits there.
func runFork(loaded *config.Loaded, spec string, opts sendOpts) {
	stay, asJSON := opts.stay, opts.json
	target, at, perr := parseTarget(spec)
	if perr != nil {
		dieUsage("fork: %s", perr)
	}

	WithAngelus(loaded, func(acli *sdk.Angelus) error {
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

		resp, err := waitForFork(ctx, acli, target, at, opts.outfit, "")
		if err != nil {
			die("fork: %s", err)
		}

		// Bind clears any pending fork point on the binding, and a cauterized
		// fork may hand back a continuation elsewhere.
		rescoped := false
		if target == bound && !stay {
			if err := bindBinding(ctx, acli, ppid, resp.Continuation, 0); err != nil {
				fmt.Fprintf(stderrw, "warning: could not bind shell to continuation: %s\n", err)
			} else {
				rescoped = true
			}
		}

		if asJSON {
			// aria_id is the continuation when this shell moved, else
			// the alternative.
			ariaID := resp.Alternative
			if rescoped {
				ariaID = resp.Continuation
			}
			enc := json.NewEncoder(stdout)
			_ = enc.Encode(struct {
				AriaID       string `json:"aria_id"`
				Parent       string `json:"parent"`
				Continuation string `json:"continuation"`
				Alternative  string `json:"alternative"`
				Turn         uint64 `json:"turn,omitempty"`
				Node         *int   `json:"node,omitempty"`
				Rescoped     bool   `json:"rescoped"`
				OwnerNote    string `json:"owner_note,omitempty"`
				Mode         string `json:"mode"`
			}{
				AriaID:       ariaID,
				Parent:       resp.Parent,
				Continuation: resp.Continuation,
				Alternative:  resp.Alternative,
				Turn:         at.turn,
				Node:         at.nodeJSON(),
				Rescoped:     rescoped,
				OwnerNote:    resp.OwnerNote,
				Mode:         "fork",
			})
			return nil
		}

		if resp.OwnerNote != "" {
			fmt.Fprintf(stderrw, "%s\n", resp.OwnerNote)
		}
		contNote := "(attend to continue)"
		if rescoped {
			contNote = "(this shell)"
		}
		fmt.Fprintf(stderrw,
			"forked %s at %s\n  continuation %s  %s\n  alternative  %s  (attend it to diverge)\n",
			resp.Parent, at, resp.Continuation, contNote, resp.Alternative)
		return nil
	})
}

// runNormalize makes every aria absorb the history it is presented away from,
// so no delete owes a boundary repair.
func runNormalize(loaded *config.Loaded, segments bool) {
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
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
			fmt.Fprintln(stderrw, "normalize: already normalized, nothing to absorb")
		case 1:
			fmt.Fprintln(stderrw, "normalize: 1 aria now owns its history outright")
		default:
			fmt.Fprintf(stderrw, "normalize: %d arias now own their history outright\n", resp.Detached)
		}
		return nil
	})
}

// runPromote changes only the presentation hierarchy: nothing moves on disk.
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
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
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
		fmt.Fprintf(stderrw, "promoted %s by %d level(s): `figaro ls` now draws it there\n", target, resp.Climbed)
		return nil
	})
}

// A bare id pins the trunk's leaf; an LT pins a pending fork point; :<LT>
// re-pins the bound aria.
func runAttend(loaded *config.Loaded, spec string) {
	// A statically-attended shell (FIGARO_ARIA) has an identity, not a binding.
	if id := envAriaID(); id != "" {
		die("attend: this shell is statically attended to %s (FIGARO_ARIA) and cannot be re-attended.\n"+
			"  figaro send --id <id> -- <prompt>   talk to another aria\n"+
			"  figaro show --id <id>               read another aria", id)
	}
	if bindingDisabled() {
		die("attend: binding disabled (--no-bind, FIGARO_NO_BIND, or non-interactive shell); this command has no effect here")
	}
	WithAngelus(loaded, func(acli *sdk.Angelus) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := attendVerb(ctx, verbEnv{loaded: loaded, acli: acli, shellPID: shellPID}, spec)
		if err != nil {
			die("attend: %s", err)
		}
		if out.Note != "" {
			fmt.Fprintf(stderrw, "%s\n", out.Note)
		}
		if out.Home {
			return nil
		}
		if !out.At.isHead() {
			fmt.Fprintf(stderrw, "attending %s at %s (next prompt forks there)\n", out.ID, out.At)
		} else {
			fmt.Fprintf(stderrw, "attending %s\n", out.ID)
		}
		return nil
	})
}
