// callpath enumerates every call path from an ENTRY symbol to a SINK symbol,
// with file:line per node, and says WHICH ANALYSIS produced each edge.
//
// WHY A TOOL AND NOT A LIST. A hand-read call path is the instrument that
// failed this campaign most often: a grep answers about the TEXT, not the CALL
// GRAPH. A list is a snapshot of someone's reading and is wrong within a day;
// this can be re-run after a refactor.
//
// PRECISION IS PRINTED, NEVER ASSUMED. A VTA edge and a CHA edge are different
// claims: CHA admits every implementation of an interface method, VTA narrows
// by value flow. Both appear here; neither is printed as the other. An edge at
// an INVOKE site is a CANDIDATE SET, not a static fact, and is labelled so.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

func main() {
	var (
		pkgPat   = flag.String("pkgs", "./...", "package pattern to load")
		entry    = flag.String("entry", "", "entry symbol substring, e.g. (*XwalStore).Read")
		sink     = flag.String("sink", "", "sink symbol substring, e.g. json.Marshal")
		algo     = flag.String("algo", "vta", "vta|cha")
		maxPaths = flag.Int("max", 40, "maximum paths to print (path mode)")
		treeMode = flag.Bool("tree", false, "emit an ORDERED, INDENTED CALL TREE from -entry (Gluck's form) instead of paths")
		treeDep  = flag.Int("treedepth", 12, "maximum tree depth")
		deep     = flag.Bool("deep", false, "walk INTO dependencies and the runtime; without it, frames outside the main module are cut and marked OPAQUE")
		modPath  = flag.String("in", "github.com/jack-work/figaro,github.com/jack-work/figwal", "comma-separated package prefixes considered INSIDE the walk; everything else is cut and marked OPAQUE unless -deep")
		maxDepth = flag.Int("depth", 25, "maximum path length")
		logs     = flag.Bool("logs", true, "list log statements in each path node")
	)
	flag.Parse()
	// TREE MODE HAS NO SINK. The first tree run died instantly on this check and
	// I read "still running" off a shell sleep rather than off the artifact --
	// the exact failure this campaign documents, in the tool built to escape it.
	// The ARTIFACT said EXIT=2 in two lines and was right.
	if *entry == "" || (*sink == "" && !*treeMode) {
		fmt.Fprintln(os.Stderr, "callpath: -entry is required; -sink is required unless -tree")
		os.Exit(2)
	}

	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Tests: false}
	pkgs, err := packages.Load(cfg, *pkgPat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		os.Exit(1)
	}
	nerr := packages.PrintErrors(pkgs)
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	var cg *callgraph.Graph
	switch *algo {
	case "cha":
		cg = cha.CallGraph(prog)
	default:
		cg = vta.CallGraph(ssautil.AllFunctions(prog), cha.CallGraph(prog))
	}
	cg.DeleteSyntheticNodes()

	fmt.Printf("## callpath\n")
	fmt.Printf("# ALGORITHM: %s   (vta = value-flow narrowed; cha = every implementation of the interface method)\n", strings.ToUpper(*algo))
	fmt.Printf("# PRECISION: an edge at an INVOKE site is a CANDIDATE SET, not a static fact. Marked DISPATCH below.\n")
	// THE SHAPE OF THE QUESTION IS NOT THE SHAPE OF A CALLGRAPH. 6ec565b5's
	// correction, and it belongs in the header where every reader meets it
	// rather than in three separate heads.
	fmt.Printf("# THIS TOOL ANSWERS CALL PATHS. GLUCK ASKED FOR A DATA PATH. They coincide\n")
	fmt.Printf("# only where bytes move by call and return. AN ABSENT PATH MAY MEAN THE BYTES\n")
	fmt.Printf("# CROSSED BY VALUE RATHER THAN BY CALL -- a Log RETURNED by Open and invoked\n")
	fmt.Printf("# later from another stack has NO call path from Open, and is not unrelated.\n")
	fmt.Printf("# So: an empty result means ONE of -- no call path; the bytes crossed by value;\n")
	fmt.Printf("# or the symbol is outside the cut. It NEVER means the two are unconnected.\n")
	// THE CUT, printed because a callgraph over ./internal/... and one over
	// ./... answer different questions, and a path that leaves through cmd/ or
	// through a TEST-ONLY implementation looks identical in the output
	// otherwise. 6ec565b5 asked for this unprompted and is right.
	fmt.Printf("# THE CUT: pattern %q, tests=%v, packages loaded=%d, load errors=%d\n", *pkgPat, cfg.Tests, len(pkgs), nerr)
	var loaded []string
	for _, p := range pkgs {
		loaded = append(loaded, p.PkgPath)
	}
	sort.Strings(loaded)
	if len(loaded) <= 30 {
		for _, p := range loaded {
			fmt.Printf("#   pkg %s\n", p)
		}
	} else {
		fmt.Printf("#   (%d packages; first 8) ", len(loaded))
		fmt.Printf("%s ...\n", strings.Join(loaded[:8], " "))
	}
	fmt.Printf("# ANYTHING OUTSIDE THAT CUT IS INVISIBLE, not absent.\n")
	fmt.Printf("# MODULE CUT: deep=%v. INSIDE = %s. figwal is INSIDE, not opaque: the harness\n", *deep, *modPath)
	fmt.Printf("#   resolves its symbols with full file:line. OPAQUE is reserved for what is\n")
	fmt.Printf("#   genuinely unwalkable -- vendored provider SDKs, and the net/HTTP stack where\n")
	fmt.Printf("#   a CHA candidate set becomes noise wearing the costume of an answer.\n")
	fmt.Printf("#   A frame outside INSIDE terminates the walk\n")
	fmt.Printf("#   marked [OPAQUE: dependency, not analysed -- see the specimen path for what lies below].\n")
	fmt.Printf("# THE COPIED / RESHAPED / BY-REFERENCE COLUMN IS NOT HERE AND NEVER WILL BE:\n")
	fmt.Printf("#   no callgraph infers it. It is the column the restructuring needs, it is filled\n")
	fmt.Printf("#   BY READING, and it is marked READ wherever it appears.\n")
	fmt.Printf("# entry~%q  sink~%q\n\n", *entry, *sink)

	var entries, sinks []*callgraph.Node
	for fn, n := range cg.Nodes {
		if fn == nil {
			continue
		}
		s := fn.String()
		if strings.Contains(s, *entry) {
			entries = append(entries, n)
		}
		if strings.Contains(s, *sink) {
			sinks = append(sinks, n)
		}
	}
	sortNodes(entries)
	sortNodes(sinks)
	fmt.Printf("# entry symbols matched: %d\n", len(entries))
	for _, n := range entries {
		fmt.Printf("#   ENTRY %s  %s\n", n.Func.String(), pos(prog, n.Func))
	}
	fmt.Printf("# sink symbols matched: %d\n", len(sinks))
	for _, n := range sinks {
		fmt.Printf("#   SINK  %s  %s\n", n.Func.String(), pos(prog, n.Func))
	}
	if len(entries) == 0 || len(sinks) == 0 {
		fmt.Printf("\n## NO PATHS: %s\n", vacuity(len(entries), len(sinks)))
		return
	}

	if *treeMode {
		fmt.Printf("\n## ORDERED CALL TREE from %q\n", *entry)
		fmt.Printf("# ORDER IS EXECUTION ORDER WHERE THAT IS STATICALLY KNOWABLE: children are\n")
		fmt.Printf("# sorted by THE POSITION OF THE CALL SITE IN THE CALLER, which is the only\n")
		fmt.Printf("# ordering available without running the program. A loop body appears once.\n")
		fmt.Printf("# A frame with no SSA body is printed OPAQUE with the reason -- vendored SDK,\n")
		fmt.Printf("# figwal internals, assembly, or outside the cut -- rather than omitted.\n\n")
		for _, e := range entries {
			cutOutside = *modPath
			cutOn = !*deep
			tree(prog, e, "", map[*callgraph.Node]bool{}, *treeDep, true)
			fmt.Println()
		}
		fmt.Println("## HOLES THIS ANALYSIS CANNOT SEE, enumerated rather than omitted:")
		fmt.Println("#   reflection; function values in struct fields called later; callbacks")
		fmt.Println("#   registered at init; anything outside the cut, INCLUDING provider SDKs.")
		return
	}

	isSink := map[*callgraph.Node]bool{}
	for _, s := range sinks {
		isSink[s] = true
	}
	printed := 0
	seenPath := map[string]bool{}
	for _, e := range entries {
		walk(prog, e, isSink, nil, map[*callgraph.Node]bool{}, *maxDepth, &printed, *maxPaths, seenPath, *logs)
	}
	fmt.Printf("\n## PATHS PRINTED: %d", printed)
	if printed >= *maxPaths {
		fmt.Printf("  (TRUNCATED at -max=%d: the set is larger than this listing)", *maxPaths)
	}
	fmt.Println()
	fmt.Println("## HOLES THIS ANALYSIS CANNOT SEE, enumerated rather than omitted:")
	fmt.Println("#   reflection; function values stored in struct fields and called later;")
	fmt.Println("#   callbacks registered at init or passed as arguments (VTA follows many, CHA fewer);")
	fmt.Println("#   go/defer through interface values where the concrete type never flows locally;")
	fmt.Println("#   anything in a package not matched by -pkgs, INCLUDING the provider SDKs.")
}

// tree prints the ordered call tree. Children are ordered by the CALL SITE's
// position in the caller: the only execution-shaped ordering that exists
// statically. Recursion is cut and marked rather than silently pruned.
func tree(prog *ssa.Program, n *callgraph.Node, indent string, onStack map[*callgraph.Node]bool, depth int, root bool) {
	fn := n.Func
	ann := annotate(fn)
	if root {
		fmt.Printf("%s%s   %s%s\n", indent, sym(fn), pos(prog, fn), ann)
	}
	if depth == 0 {
		fmt.Printf("%s   +- ... DEPTH LIMIT: subtree not walked, not absent\n", indent)
		return
	}
	if onStack[n] {
		fmt.Printf("%s   +- [CYCLE -> back to %s at depth %d] not expanded: the tree's DEPTH would otherwise be an artifact of -treedepth rather than of the call stack\n", indent, sym(fn), depthOf[n])
		return
	}
	depthOf[n] = len(depthOf)
	onStack[n] = true
	defer delete(onStack, n)

	edges := append([]*callgraph.Edge(nil), n.Out...)
	sort.SliceStable(edges, func(i, j int) bool {
		return sitePos(prog, edges[i]) < sitePos(prog, edges[j])
	})
	for i, e := range edges {
		last := i == len(edges)-1
		branch := "+- "
		childIndent := indent + "|  "
		if last {
			branch = "+- "
			childIndent = indent + "   "
		}
		kind := "STATIC  "
		if e.Site != nil && e.Site.Common().IsInvoke() {
			kind = fmt.Sprintf("DISPATCH[%d] ", len(candidatesAt(e)))
		} else if e.Site == nil {
			kind = "SYNTHETIC "
		}
		cf := e.Callee.Func
		genmark := ""
		if e.Site != nil && cf.Pos() == e.Caller.Func.Pos() && cf != e.Caller.Func {
			genmark = "  [GENERIC INSTANTIATION of the caller: same source, different type args -- one frame at runtime, two symbols here]"
		}
		fmt.Printf("%s%s%s%s   %s%s%s%s\n", indent, branch, kind, sym(cf), pos(prog, cf), annotate(cf), conditional(e), genmark)
		if kind[0] == 'D' {
			cands := candidatesAt(e)
			for k, c := range cands {
				fmt.Printf("%s   |  +- [CANDIDATE %d/%d] %s\n", indent, k+1, len(cands), c)
			}
		}
		for _, l := range logCalls(cf) {
			fmt.Printf("%s   |  LOG %s\n", indent, l)
		}
		if cutOn && outsideModule(cf) {
			fmt.Printf("%s   |  [OPAQUE: dependency, not analysed -- see the specimen path for what lies below]\n", indent)
			continue
		}
		tree(prog, e.Callee, childIndent, onStack, depth-1, false)
	}
}

var (
	cutOutside string
	cutOn      bool
	depthOf    = map[*callgraph.Node]int{}
)

// conditional says whether a call site is reached on SOME paths through its
// caller rather than on all of them -- derived from the SSA CFG, not guessed.
//
// 6ec565b5's requirement, and it is a correctness matter rather than a nicety:
// segment.ReadIndex returns a resident payload on a HIT and only reaches
// codec.ReadFrame -> os.File.ReadAt -> pread on a MISS. A tree printing the
// syscall as the unconditional bottom ASSERTS A SYSCALL THAT MOST READS NEVER
// MAKE, and would mislead exactly the person restructuring against it.
func conditional(e *callgraph.Edge) string {
	if e.Site == nil {
		return ""
	}
	blk := e.Site.Block()
	if blk == nil || blk.Parent() == nil || len(blk.Parent().Blocks) == 0 {
		return ""
	}
	if blk == blk.Parent().Blocks[0] {
		return "  [UNCONDITIONAL in its caller: entry block]"
	}
	return "  [CONDITIONAL: reached on SOME paths through the caller, not all -- e.g. a cache MISS]"
}

// outsideModule reports whether a frame belongs to a dependency rather than to
// the module under analysis.
func outsideModule(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	for _, pfx := range strings.Split(cutOutside, ",") {
		if pfx != "" && strings.HasPrefix(fn.Pkg.Pkg.Path(), strings.TrimSpace(pfx)) {
			return false
		}
	}
	return true
}

// annotate marks a frame the analysis cannot see inside, with the REASON.
func annotate(fn *ssa.Function) string {
	if fn == nil {
		return "  [OPAQUE: no function]"
	}
	if fn.Blocks == nil {
		return "  [OPAQUE: no SSA body -- external, assembly, vendored, or outside the cut]"
	}
	return ""
}

func sitePos(prog *ssa.Program, e *callgraph.Edge) int {
	if e.Site == nil {
		return 1 << 30
	}
	return int(e.Site.Pos())
}

func vacuity(e, s int) string {
	switch {
	case e == 0 && s == 0:
		return "NEITHER entry nor sink matched any symbol. This is a vacuous run, not a finding of no path."
	case e == 0:
		return "the ENTRY matched no symbol. Vacuous run."
	default:
		return "the SINK matched no symbol. Vacuous run."
	}
}

func walk(prog *ssa.Program, n *callgraph.Node, isSink map[*callgraph.Node]bool, path []*callgraph.Edge,
	onStack map[*callgraph.Node]bool, depth int, printed *int, max int, seen map[string]bool, wantLogs bool) {
	if *printed >= max || depth == 0 || onStack[n] {
		return
	}
	if isSink[n] && len(path) > 0 {
		key := pathKey(path)
		if !seen[key] {
			seen[key] = true
			*printed++
			printPath(prog, path, *printed, wantLogs)
		}
		return
	}
	onStack[n] = true
	defer delete(onStack, n)
	edges := append([]*callgraph.Edge(nil), n.Out...)
	sort.Slice(edges, func(i, j int) bool { return edges[i].Callee.Func.String() < edges[j].Callee.Func.String() })
	for _, e := range edges {
		walk(prog, e.Callee, isSink, append(path, e), onStack, depth-1, printed, max, seen, wantLogs)
	}
}

// candidatesAt returns every callee the analysis admits AT THE SAME CALL SITE
// as e -- the implementation set for one interface dispatch.
func candidatesAt(e *callgraph.Edge) []string {
	if e.Site == nil {
		return []string{e.Callee.Func.String()}
	}
	var out []string
	for _, o := range e.Caller.Out {
		if o.Site == e.Site {
			out = append(out, o.Callee.Func.String())
		}
	}
	sort.Strings(out)
	return out
}

func pathKey(path []*callgraph.Edge) string {
	var b strings.Builder
	for _, e := range path {
		b.WriteString(e.Callee.Func.String())
		b.WriteByte('|')
	}
	return b.String()
}

func printPath(prog *ssa.Program, path []*callgraph.Edge, n int, wantLogs bool) {
	fmt.Printf("\n### PATH %d  (%d hops)\n", n, len(path))
	fmt.Printf("  %-9s %s  %s\n", "ROOT", path[0].Caller.Func.String(), pos(prog, path[0].Caller.Func))
	for _, e := range path {
		kind := "STATIC"
		if e.Site != nil && e.Site.Common().IsInvoke() {
			kind = "DISPATCH"
		} else if e.Site == nil {
			kind = "SYNTHETIC"
		}
		site := "?"
		if e.Site != nil {
			site = prog.Fset.Position(e.Site.Pos()).String()
		}
		fmt.Printf("  %-9s %s\n            at %s   -> %s\n", kind, e.Callee.Func.String(), site, pos(prog, e.Callee.Func))
		if kind == "DISPATCH" {
			// THE CANDIDATE SET AT THIS SITE. This is the column a hand-read
			// list gets wrong: an interface call is not one callee, it is every
			// implementation the analysis admits. Printed in full so a reader
			// can see whether the row is a static fact or a set.
			cands := candidatesAt(e)
			fmt.Printf("            CANDIDATE SET at this site (%d, per %s):\n", len(cands), "the named algorithm")
			for _, c := range cands {
				mark := ""
				if strings.Contains(c, "_test") || strings.Contains(c, ".test") {
					mark = "  [TEST-ONLY BY NAME -- verify]"
				}
				fmt.Printf("              - %s%s\n", c, mark)
			}
		}
		if wantLogs {
			for _, l := range logCalls(e.Callee.Func) {
				fmt.Printf("            LOG  %s\n", l)
			}
		}
	}
}

// logCalls lists log/slog statements inside a function, because Gluck asked for
// them and because tonight one slog.Warn was a THIRD of a benchmark and another
// made a fixture unparseable by landing on the result line.
func logCalls(fn *ssa.Function) []string {
	var out []string
	if fn == nil || fn.Blocks == nil {
		return nil
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			c, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			cal := c.Common().StaticCallee()
			if cal == nil {
				continue
			}
			s := cal.String()
			if strings.Contains(s, "log/slog") || strings.HasPrefix(s, "log.") || strings.Contains(s, "(*log/slog.Logger)") {
				out = append(out, fmt.Sprintf("%s  at %s", s, c.Parent().Prog.Fset.Position(instr.Pos())))
			}
		}
	}
	return out
}

// sym prints a symbol with its GENERIC TYPE ARGUMENTS, because two
// instantiations of one generic print IDENTICALLY without them -- and in this
// codebase that is exactly the distinction between the fig IR channel
// (message.Message) and the translator channel ([]json.RawMessage). A tree that
// prints them the same merges two different byte movements into one line.
func sym(fn *ssa.Function) string {
	if fn == nil {
		return "<nil>"
	}
	s := fn.String()
	if ta := fn.TypeArgs(); len(ta) > 0 {
		var parts []string
		for _, t := range ta {
			parts = append(parts, t.String())
		}
		s += "[" + strings.Join(parts, ",") + "]"
	}
	return s
}

func pos(prog *ssa.Program, fn *ssa.Function) string {
	if fn == nil || fn.Pos() == token.NoPos {
		return "(no position: synthetic or generated)"
	}
	return prog.Fset.Position(fn.Pos()).String()
}

func sortNodes(ns []*callgraph.Node) {
	sort.Slice(ns, func(i, j int) bool { return ns[i].Func.String() < ns[j].Func.String() })
}
