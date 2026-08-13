package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/largo"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/term"
)

// runShow handles `figaro show [--id <id>] [N] [-v|-l|-a]`.
func runShow(loaded *config.Loaded, idFlag string, args []string) {
	renderAria(loaded, idFlag, args)
}

// showOpts is the parsed flag state of `figaro show`.
type showOpts struct {
	last     int // last N units (default 10)
	from, to int // unit-index range; -1 = unset
	before   int // keyset pagination: show N entries before this LT; -1 = unset
	all      bool
	jsonOut  bool
	verbose  bool
	literal  bool
	details  bool // -o: the metadata Ctrl-O shows in the pager
	maxBytes int  // --max-bytes: paint budget; the OLDEST turns are dropped first. 0 = none
}

// renderAria prints history for an aria. The default view derives the
// conversational units from the IR and renders them through the node
// widget renderer. --json emits those units verbatim (materialized, no
// delta compression). N / --last N / --from A [--to B] / --all select a
// unit range. --verbose and --literal use the raw IR path.
func renderAria(loaded *config.Loaded, id string, args []string) {
	opts := parseShowArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	figaroID := id
	if figaroID == "" {
		r, err := resolveBinding(ctx, acli, shellPID)
		if err != nil {
			die("resolve: %s", err)
		}
		if !r.Found {
			die("no figaro bound to this shell")
		}
		figaroID = r.FigaroID
	}

	// Read the IR through the angelus's shared LogCache, walking BACKWARD from
	// the tail until the selector is covered (see show_window.go). One forward
	// read from LT 0 is what this used to do, and the angelus caps a single
	// read at 1000 entries, so a long aria was shown its own prefix and told
	// that was all of it.
	//
	// SEAM (Phase 3): the walk still derives turns client-side, because turns
	// are not derivable from an LT window without composing one. The paginated
	// turn-aware read (Page/TurnPart, byte budget, bidirectional) replaces the
	// walk wholesale; when it lands, `show` becomes a thin client of it.
	w, err := gatherShowWindow(ctx, acli, figaroID, opts)
	if err != nil {
		die("aria.read: %s", err)
	}
	// A pre-turn-id aria can only be numbered from its head: see derivedIDs.
	if !w.atHead && derivedIDs(w.entries) {
		resp, rerr := acli.AriaRead(ctx, figaroID, 0, 0)
		if rerr != nil {
			die("aria.read: %s", rerr)
		}
		entries := make([]store.Entry[message.Message], len(resp.Entries))
		for i, e := range resp.Entries {
			entries[i].LT = e.LT
			if uerr := json.Unmarshal(e.Payload, &entries[i].Payload); uerr != nil {
				die("aria.read: parse LT=%d: %s", e.LT, uerr)
			}
		}
		w = showWindow{entries: entries, total: resp.Total, atHead: len(entries) >= resp.Total, pages: 1, fromHead: true}
		if !w.atHead {
			showNote("this aria stores no turn ids (written before they existed), so its turns can only be counted from the head: showing the first %d of %d entries", len(w.entries), w.total)
		}
	}
	if len(w.entries) == 0 && w.total == 0 {
		fmt.Fprintln(os.Stderr, "(empty aria)")
		return
	}
	entries := w.entries

	// --verbose / --literal: the raw IR path (inline transitions + extras,
	// or unrendered IR markdown).
	if opts.verbose || opts.literal {
		renderAriaIR(loaded, figaroID, w, opts)
		return
	}

	// Default + --json: conversational units derived from the IR. A window
	// that does not start at the head drops its oldest turn: the page boundary
	// fell inside it, and a turn without its prompt renders as an answer to
	// nothing.
	turns := trimPartialHead(composeTurns(entries), w.atHead)
	lo, hi := selectTurnRange(turns, opts)
	if lo < hi && opts.maxBytes > 0 {
		if _, dropped := clipToBudget(turns[lo:hi], opts.maxBytes); dropped > 0 {
			showNote("--max-bytes %d dropped the %d oldest turn(s) of this selection; a budget clips the head, never the tail", opts.maxBytes, dropped)
			lo += dropped
		}
	}

	if opts.jsonOut {
		// The UI IR wire shape VERBATIM, an aria.Page, exactly what figaro.read
		// returns and figaro.aria pushes. Not a bare []Turn, not a shadow struct:
		// the same bytes a client folds, so `show --json` and the live stream can
		// never describe a conversation differently.
		page := aria.Page{Parts: make([]aria.TurnPart, 0, hi-lo)}
		for _, t := range turns[lo:hi] {
			// Whole turns: `show` selects by turn, so no part is ever clipped.
			page.Parts = append(page.Parts, aria.TurnPart{Turn: t, From: 0})
		}
		// Before is true when anything older exists, whether it was sliced
		// off here or simply never walked to.
		page.More = aria.More{Before: lo > 0 || !w.atHead, After: hi < len(turns)}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(page); err != nil {
			die("json: %s", err)
		}
		return
	}

	width := termWidth()
	// Label the window by TURN ID, not by slice position. They coincide only
	// while ids are gapless and 1-based; printing lo+1 here would be a fourth
	// coordinate system in the one command that just collapsed three.
	if lo >= hi {
		if w.atHead {
			fmt.Printf("# aria %s: %d turns (no turn in range)\n\n", figaroID, len(turns))
		} else if w.fromHead {
			fmt.Printf("# aria %s: no turn in that range within the first %d turns\n\n", figaroID, len(turns))
		} else {
			// The count is NOT known here: the walk stopped as soon as the
			// selector was covered, so len(turns) is the size of the window,
			// not of the aria. Printing it as a total restates the old bug.
			fmt.Printf("# aria %s: no turn in that range within the %d turns walked back from the tail\n\n", figaroID, len(turns))
		}
		return
	}
	if w.atHead {
		fmt.Printf("# aria %s: %d turns (showing %d..%d) · [N] is the turn to fork/send at\n\n", figaroID, len(turns), turns[lo].ID, turns[hi-1].ID)
	} else if w.fromHead {
		// The legacy path: a forward read that stopped at the angelus's cap.
		// Say FIRST, because that is what it is.
		fmt.Printf("# aria %s: turns %d..%d, the first %d of %d entries · [N] is the turn to fork/send at\n\n", figaroID, turns[lo].ID, turns[hi-1].ID, len(w.entries), w.total)
	} else {
		fmt.Printf("# aria %s: turns %d..%d, walked back from the tail of %d entries · [N] is the turn to fork/send at\n\n", figaroID, turns[lo].ID, turns[hi-1].ID, w.total)
	}
	for i := lo; i < hi; i++ {
		u := turns[i]
		fmt.Println(term.Dim(fmt.Sprintf("[%d]", u.ID)))
		fmt.Println()
		// The DEFAULT is what `figaro listen` draws: the same composer, the
		// same rows, no metadata. `-o` adds the addresses and timestamps Ctrl-O
		// shows in the pager: it used to be on unconditionally, which meant
		// every `show` printed detail nobody had asked for.
		rows := renderTurnRows(aria.Message{
			Turn: int(u.ID), Role: livedoc.RoleOutput,
			Inquiry: u.Inquiry, InquirySegments: u.InquirySegments,
			FormDeltas: u.FormDeltas, Nodes: u.Nodes,
		}, width, 0, renderSettings{verbose: opts.details})
		auditRows(rows, width, "show")
		fmt.Println(strings.Join(rows, "\n"))
		fmt.Println()
	}
}

// parseShowArgs turns `show`'s reassembled argv into showOpts.
//
// It is a named function, not a loop inside renderAria, because the flag most
// recently broken here was broken by NOT BEING PARSED: cmdkit consumed
// --max-bytes, the reassembly in cli.go did not pass it on, and `show`
// exited 0 having ignored it. A parser you cannot call is a parser you cannot
// test, and the unit test for the clipper it feeds passed the entire time.
func parseShowArgs(args []string) showOpts {
	opts := showOpts{last: 10, from: -1, to: -1, before: -1}

	expanded := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' { // expand bundled bool shorts (-vj)
			for _, r := range a[1:] {
				expanded = append(expanded, "-"+string(r))
			}
			continue
		}
		expanded = append(expanded, a)
	}
	needInt := func(i int) int {
		if i+1 >= len(expanded) {
			die("show: %s requires a value", expanded[i])
		}
		return mustAtoi(expanded[i+1])
	}
	for i := 0; i < len(expanded); i++ {
		a := expanded[i]
		switch {
		case a == "-o" || a == "--details":
			opts.details = true
		case a == "-v" || a == "--verbose":
			opts.verbose = true
		case a == "-l" || a == "--literal":
			opts.literal = true
		case a == "-a" || a == "--all":
			opts.all = true
		case a == "-j" || a == "--json":
			opts.jsonOut = true
		case a == "--from":
			opts.from = needInt(i)
			i++
		case a == "--to":
			opts.to = needInt(i)
			i++
		case a == "--max-bytes":
			opts.maxBytes = needInt(i)
			i++
		case a == "--last":
			opts.last = needInt(i)
			i++
		case strings.HasPrefix(a, "--from="):
			opts.from = mustAtoi(strings.TrimPrefix(a, "--from="))
		case strings.HasPrefix(a, "--to="):
			opts.to = mustAtoi(strings.TrimPrefix(a, "--to="))
		case strings.HasPrefix(a, "--last="):
			opts.last = mustAtoi(strings.TrimPrefix(a, "--last="))
		case a == "--before":
			opts.before = needInt(i)
			i++
		case strings.HasPrefix(a, "--before="):
			opts.before = mustAtoi(strings.TrimPrefix(a, "--before="))
		default:
			n, err := strconv.Atoi(a)
			if err != nil {
				die("usage: figaro show [--id <id>] [N | --last N | --from A [--to B] | -a] [-j|--json] [-o] [-v] [-l]")
			}
			opts.last = n
		}
	}

	return opts
}

func mustAtoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		die("show: want an integer, got %q", s)
	}
	return v
}

// selectUnitRange resolves the [lo,hi) unit window from the flags:
// selectTurnRange picks the slice of turns to show. Every selector speaks TURN
// IDs: the same coordinate `fig send <aria>:<turn>` takes and the same one the
// header prints. Before this there were three coordinate systems in one
// command: --from/--to were slice indices, --before was an LT, and the printed
// label was a third thing. Now there is one.
//
//	--all           everything
//	--from A --to B turns A..B inclusive
//	--before T -n N the N turns ending just before turn T (paginate backwards)
//	default  -n N   the last N turns: paginate backwards from the end
//
// Pagination is bidirectional by construction: --from walks forward from an
// anchor, --before walks backward from one, so a scrolling client can pull an
// earlier or a later page from wherever it is.
func selectTurnRange(turns []aria.Turn, o showOpts) (int, int) {
	total := len(turns)
	if o.all {
		return 0, total
	}
	// Turn id -> slice index. Turns are in order, so a scan is enough and it
	// tolerates gaps (a forked trunk need not start at turn 1).
	indexOf := func(id uint64) int {
		for i := range turns {
			if turns[i].ID >= id {
				return i
			}
		}
		return total
	}
	if o.before >= 0 {
		hi := indexOf(uint64(o.before))
		lo := 0
		if o.last > 0 && hi > o.last {
			lo = hi - o.last
		}
		return lo, hi
	}
	if o.from >= 0 || o.to >= 0 {
		lo := 0
		if o.from > 0 {
			lo = indexOf(uint64(o.from))
		}
		hi := total
		if o.to >= 0 {
			hi = indexOf(uint64(o.to) + 1)
		}
		if hi < lo {
			hi = lo
		}
		return lo, hi
	}
	lo := 0
	if o.last > 0 && total > o.last {
		lo = total - o.last
	}
	return lo, total
}

// renderAriaIR is the raw IR path for --verbose / --literal: it renders
// each message (markdown via largo, or unrendered when --literal) and, in
// verbose mode, appends credo / state transitions / form.
func renderAriaIR(loaded *config.Loaded, figaroID string, win showWindow, opts showOpts) {
	entries := win.entries
	start := 0
	if !opts.all && len(entries) > opts.last {
		start = len(entries) - opts.last
	}

	var w io.Writer = os.Stdout
	flush := func() {}
	if !opts.literal {
		sw, err := largo.NewWriter(os.Stdout, largo.Options{})
		if err != nil {
			die("largo: %s", err)
		}
		w = sw
		flush = func() { sw.Flush() }
	}

	// The denominator is the ARIA's message count, not the window's. A tail
	// window that reported its own size as the total is exactly how `show`
	// used to claim a 4000 message aria had 1000 messages in it.
	fmt.Fprintf(w, "# aria %s: showing %d of %d messages\n\n", figaroID, len(entries)-start, win.total)
	for _, e := range entries[start:] {
		renderMessage(w, e.Payload, e.LT, opts.verbose)
	}
	flush()

	if !opts.verbose {
		return
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println("## credo")
	fmt.Println()
	// The form is a reducible channel now (there is no the form channel);
	// fetch the live snapshot through the angelus. Best-effort: the credo and
	// form sections degrade to empty rather than aborting the dump.
	snap := fetchFormSnapshot(loaded, figaroID)
	if raw, ok := snap.Get("system.credo"); ok {
		// system.credo may be a bare string or a ContentEnvelope object
		// ({content, frontmatter, filePath}). Prefer content, fall back to
		// frontmatter, then to the raw string.
		var env struct {
			Content     string `json:"content,omitempty"`
			Frontmatter string `json:"frontmatter,omitempty"`
		}
		switch {
		case json.Unmarshal(raw, &env) == nil && env.Content != "":
			fmt.Println(env.Content)
		case env.Frontmatter != "":
			fmt.Println(env.Frontmatter)
		default:
			var s string
			if json.Unmarshal(raw, &s) == nil {
				fmt.Println(s)
			} else {
				fmt.Println(string(raw))
			}
		}
	} else {
		fmt.Println("(no system.credo on form)")
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println("## state transitions")
	fmt.Println()
	printTransitions(os.Stdout, entries)
	fmt.Println()
	fmt.Println("---")
	fmt.Println("## form")
	fmt.Println()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(nestSnapshot(snap))
}

// printTransitions prints all form patches in LT order.
func printTransitions(w io.Writer, entries []store.Entry[message.Message]) {
	any := false
	for _, e := range entries {
		for _, p := range e.Payload.Patches {
			if p.IsEmpty() {
				continue
			}
			any = true
			fmt.Fprintf(w, "#%d (%s):\n", e.LT, e.Payload.Role)
			keys := make([]string, 0, len(p.Set))
			for k := range p.Set {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "  set %s = %s\n", k, truncate(string(p.Set[k]), 400))
			}
			for _, k := range p.Remove {
				fmt.Fprintf(w, "  remove %s\n", k)
			}
		}
	}
	if !any {
		fmt.Fprintln(w, "(no patches)")
	}
}

// renderMessage writes one IR message as markdown.
func renderMessage(w io.Writer, m message.Message, lt uint64, verbose bool) {
	switch m.Role {
	case message.RoleInput:
		var text string
		var toolResults []message.Content
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentProse:
				if text != "" {
					text += "\n\n"
				}
				text += c.Text
			case message.ContentToolResult:
				toolResults = append(toolResults, c)
			}
		}
		if text != "" {
			fmt.Fprintf(w, "**input** [#%d]\n\n> %s\n\n", lt, indentBlockquote(text))
		}
		for _, c := range toolResults {
			marker := "↩"
			if c.IsError {
				marker = "⚠"
			}
			fmt.Fprintf(w, "%s **%s** result\n\n```\n%s\n```\n\n", marker, c.ToolName, truncate(c.Text, 800))
		}
		if verbose && len(m.Patches) > 0 {
			fmt.Fprintf(w, "*state transition [#%d]:*\n\n", lt)
			for _, p := range m.Patches {
				keys := make([]string, 0, len(p.Set))
				for k := range p.Set {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(w, "- set `%s` = `%s`\n", k, truncate(string(p.Set[k]), 400))
				}
				for _, k := range p.Remove {
					fmt.Fprintf(w, "- remove `%s`\n", k)
				}
			}
			fmt.Fprint(w, "\n")
		}

	case message.RoleOutput:
		header := fmt.Sprintf("**figaro** [#%d]", lt)
		if m.StopReason != "" {
			header += fmt.Sprintf(" *(%s)*", m.StopReason)
		}
		fmt.Fprintf(w, "%s\n\n", header)
		for _, c := range m.Content {
			switch c.Type {
			case message.ContentProse:
				fmt.Fprintf(w, "%s\n\n", c.Text)
			case message.ContentThinking:
				if verbose {
					fmt.Fprintf(w, "> *🤔 %s*\n\n", c.Text)
				}
			case message.ContentToolInvoke:
				fmt.Fprintf(w, "→ **%s** %s\n\n", c.ToolName, toolCallSummary(c))
			}
		}
		if verbose && m.Usage != nil {
			fmt.Fprintf(w, "*tokens: in=%d out=%d cache_r=%d cache_w=%d*\n\n",
				m.Usage.InputTokens, m.Usage.OutputTokens,
				m.Usage.CacheReadTokens, m.Usage.CacheWriteTokens)
		}
	}
}

func indentBlockquote(s string) string {
	return strings.ReplaceAll(s, "\n", "\n> ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (%d more bytes)", len(s)-n)
}

func toolCallSummary(c message.Content) string {
	switch c.ToolName {
	case "bash":
		if cmd, ok := c.Arguments["command"].(string); ok {
			return "`" + truncate(cmd, 120) + "`"
		}
	case "read", "write", "edit":
		if path, ok := c.Arguments["path"].(string); ok {
			return "`" + path + "`"
		}
	}
	return ""
}
