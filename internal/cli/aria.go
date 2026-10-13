package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/largo"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
	"github.com/jack-work/figaro/sdk/aria"
)

func runShow(loaded *config.Loaded, idFlag string, args []string) {
	renderAria(loaded, idFlag, args)
}

type showOpts struct {
	last     int
	from, to int
	before   int
	all      bool
	jsonOut  bool
	verbose  bool
	literal  bool
	details  bool
	maxBytes int
}

// renderAria: the default view renders the daemon's composed units; --json
// emits them verbatim; --verbose and --literal use the raw IR path.
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

	// --verbose / --literal are the raw IR view: records, not turns.
	if opts.verbose || opts.literal {
		w, err := gatherShowWindow(ctx, acli, figaroID, opts)
		if err != nil {
			die("aria.read: %s", err)
		}
		if len(w.entries) == 0 && w.total == 0 {
			fmt.Fprintln(stderrw, "(empty aria)")
			return
		}
		renderAriaIR(loaded, figaroID, w, opts)
		return
	}

	// The daemon composes; a client that built turns from raw IR would be a
	// third composition of the same data.
	w, err := gatherShowTurns(ctx, acli, figaroID, opts)
	if err != nil {
		die("read %s: %s", figaroID, err)
	}
	// A selector past the end selects nothing and still prints the empty page;
	// only an aria with no turns is empty, and prose on stdout would break a
	// json pipe.
	if len(w.turns) == 0 && opts.from < 0 && opts.before < 0 {
		fmt.Fprintln(stderrw, "(empty aria)")
		return
	}
	turns := w.turns
	lo, hi := selectShowRange(turns, opts)
	if lo < hi && opts.maxBytes > 0 {
		if _, dropped := clipToBudget(turns[lo:hi], opts.maxBytes); dropped > 0 {
			showNote("--max-bytes %d dropped the %d oldest turn(s) of this selection; a budget clips the head, never the tail", opts.maxBytes, dropped)
			lo += dropped
		}
	}

	if opts.jsonOut {
		// The wire shape verbatim: the same bytes the pager folds.
		page := aria.Page{Parts: make([]aria.TurnPart, 0, hi-lo)}
		for _, t := range turns[lo:hi] {
			page.Parts = append(page.Parts, aria.TurnPart{Turn: t, From: 0})
		}
		page.More = aria.More{Before: lo > 0 || !w.atHead, After: hi < len(turns) || !w.atTail}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(page); err != nil {
			die("json: %s", err)
		}
		return
	}

	width := termWidth()
	// The window is labelled by turn id, not slice position.
	if lo >= hi {
		if w.atHead {
			fmt.Fprintf(stdout, "# aria %s: %d turns (no turn in range)\n\n", figaroID, len(turns))
		} else {
			// The count is not known: the walk stopped once the
			// selector was covered.
			fmt.Fprintf(stdout, "# aria %s: no turn in that range within the %d turns walked back from the tail\n\n", figaroID, len(turns))
		}
		return
	}
	if w.atHead {
		fmt.Fprintf(stdout, "# aria %s: %d turns (showing %d..%d) · [N] is the turn to fork/send at\n\n", figaroID, len(turns), turns[lo].ID, turns[hi-1].ID)
	} else {
		fmt.Fprintf(stdout, "# aria %s: turns %d..%d, walked back from the tail · [N] is the turn to fork/send at\n\n", figaroID, turns[lo].ID, turns[hi-1].ID)
	}
	for i := lo; i < hi; i++ {
		u := turns[i]
		fmt.Fprintln(stdout, term.Dim(fmt.Sprintf("[%d]", u.ID)))
		fmt.Fprintln(stdout)
		// The default is what `figaro listen` draws; -o adds the metadata M-m
		// shows in the pager.
		rows := renderTurnRows(aria.Message{
			Turn: int(u.ID), Role: livedoc.RoleOutput,
			Inquiry: u.Inquiry, InquirySegments: u.InquirySegments,
			FormDeltas: u.FormDeltas, Nodes: u.Nodes,
		}, width, 0, renderSettings{verbose: opts.details, coordFormat: loaded.CoordFormat()})
		auditRows(rows, width, "show")
		fmt.Fprintln(stdout, strings.Join(rows, "\n"))
		fmt.Fprintln(stdout)
	}
}

func parseShowArgs(args []string) showOpts {
	opts := showOpts{last: 10, from: -1, to: -1, before: -1}

	expanded := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
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

// selectTurnRange: every selector speaks turn ids, the coordinate `fig send
// <aria>:<turn>` takes.
func selectTurnRange(turns []aria.Turn, o showOpts) (int, int) {
	total := len(turns)
	if o.all {
		return 0, total
	}
	// Turns are in order; a scan tolerates gaps (a forked trunk need not start
	// at turn 1).
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

	// The denominator is the aria's message count, not the window's.
	fmt.Fprintf(w, "# aria %s: showing %d of %d messages\n\n", figaroID, len(entries)-start, win.total)
	for _, e := range entries[start:] {
		renderMessage(w, e.Payload, e.LT, opts.verbose)
	}
	flush()

	if !opts.verbose {
		return
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "---")
	fmt.Fprintln(stdout, "## credo")
	fmt.Fprintln(stdout)
	// The form snapshot is best-effort: credo and form sections degrade to empty.
	snap := fetchFormSnapshot(loaded, figaroID)
	if raw, ok := snap.Get("system.credo"); ok {
		// system.credo may be a bare string or a ContentEnvelope object.
		var env struct {
			Content     string `json:"content,omitempty"`
			Frontmatter string `json:"frontmatter,omitempty"`
		}
		switch {
		case json.Unmarshal(raw, &env) == nil && env.Content != "":
			fmt.Fprintln(stdout, env.Content)
		case env.Frontmatter != "":
			fmt.Fprintln(stdout, env.Frontmatter)
		default:
			var s string
			if json.Unmarshal(raw, &s) == nil {
				fmt.Fprintln(stdout, s)
			} else {
				fmt.Fprintln(stdout, string(raw))
			}
		}
	} else {
		fmt.Fprintln(stdout, "(no system.credo on form)")
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "---")
	fmt.Fprintln(stdout, "## state transitions")
	fmt.Fprintln(stdout)
	printTransitions(os.Stdout, entries)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "---")
	fmt.Fprintln(stdout, "## form")
	fmt.Fprintln(stdout)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(nestSnapshot(snap))
}

func printTransitions(w io.Writer, entries []irEntry) {
	any := false
	for _, e := range entries {
		for _, p := range e.Payload.Patches {
			if p.IsIdentity() {
				continue
			}
			any = true
			fmt.Fprintf(w, "#%d (%s):\n", e.LT, e.Payload.Role)
			for _, ent := range p.Entries() {
				if ent.IsRemoval() {
					continue
				}
				fmt.Fprintf(w, "  set %s = %s\n", ent.Key, truncate(string(ent.New), 400))
			}
			for _, ent := range p.Entries() {
				if !ent.IsRemoval() {
					continue
				}
				k := ent.Key
				fmt.Fprintf(w, "  remove %s\n", k)
			}
		}
	}
	if !any {
		fmt.Fprintln(w, "(no patches)")
	}
}

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
				for _, ent := range p.Entries() {
					if ent.IsRemoval() {
						continue
					}
					fmt.Fprintf(w, "- set `%s` = `%s`\n", ent.Key, truncate(string(ent.New), 400))
				}
				for _, ent := range p.Entries() {
					if !ent.IsRemoval() {
						continue
					}
					k := ent.Key
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
