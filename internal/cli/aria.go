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

	"github.com/jack-work/figaro/internal/compose"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/livedoc"
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
	all      bool
	jsonOut  bool
	verbose  bool
	literal  bool
}

// renderAria prints history for an aria. The default view derives the
// conversational units from the IR and renders them through the node
// widget renderer. --json emits those units verbatim (materialized, no
// delta compression). N / --last N / --from A [--to B] / --all select a
// unit range. --verbose and --literal use the raw IR path.
func renderAria(loaded *config.Loaded, id string, args []string) {
	opts := showOpts{last: 10, from: -1, to: -1}

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
		case a == "--last":
			opts.last = needInt(i)
			i++
		case strings.HasPrefix(a, "--from="):
			opts.from = mustAtoi(strings.TrimPrefix(a, "--from="))
		case strings.HasPrefix(a, "--to="):
			opts.to = mustAtoi(strings.TrimPrefix(a, "--to="))
		case strings.HasPrefix(a, "--last="):
			opts.last = mustAtoi(strings.TrimPrefix(a, "--last="))
		default:
			n, err := strconv.Atoi(a)
			if err != nil {
				die("usage: figaro show [--id <id>] [N | --last N | --from A [--to B] | -a] [-j|--json] [-v] [-l]")
			}
			opts.last = n
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	figaroID := id
	if figaroID == "" {
		r, err := acli.Resolve(ctx, os.Getppid())
		if err != nil {
			die("resolve: %s", err)
		}
		if !r.Found {
			die("no figaro bound to this shell")
		}
		figaroID = r.FigaroID
	}

	// Read the IR through the angelus's shared LogCache (single owner of
	// the figwal log, so we don't race the live agent on the active
	// segment). The IR is canonical; the unit view is derived from it.
	resp, err := acli.AriaRead(ctx, figaroID, 0, 0)
	if err != nil {
		die("aria.read: %s", err)
	}
	if len(resp.Entries) == 0 && resp.Total == 0 {
		fmt.Fprintln(os.Stderr, "(empty aria)")
		return
	}
	entries := make([]store.Entry[message.Message], len(resp.Entries))
	for i, e := range resp.Entries {
		entries[i].LT = e.LT
		if err := json.Unmarshal(e.Payload, &entries[i].Payload); err != nil {
			die("aria.read: parse LT=%d: %s", e.LT, err)
		}
	}

	// --verbose / --literal: the raw IR path (inline transitions + extras,
	// or unrendered IR markdown).
	if opts.verbose || opts.literal {
		renderAriaIR(loaded, figaroID, entries, opts)
		return
	}

	// Default + --json: conversational units derived from the IR.
	msgs := make([]message.Message, len(entries))
	for i, e := range entries {
		msgs[i] = e.Payload
		msgs[i].LogicalTime = e.LT
	}
	units := compose.Units(msgs)
	lo, hi := selectUnitRange(len(units), opts)

	if opts.jsonOut {
		type jUnit struct {
			Index int            `json:"index"`
			Role  string         `json:"role"`
			Nodes []livedoc.Node `json:"nodes"`
		}
		out := make([]jUnit, 0, hi-lo)
		for i := lo; i < hi; i++ {
			out = append(out, jUnit{Index: i, Role: units[i].Role, Nodes: units[i].Nodes})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			die("json: %s", err)
		}
		return
	}

	width := termWidth()
	fmt.Printf("# aria %s — %d units (showing %d–%d) · [N] is the LT to fork/send at\n\n", figaroID, len(units), lo+1, hi)
	for i := lo; i < hi; i++ {
		u := units[i]
		who := "› you"
		if u.Role == "assistant" {
			who = "‹ figaro"
		}
		label := fmt.Sprintf("[%d] %s", u.LT, who)
		fmt.Println(term.Dim(label))
		fmt.Println()
		rows, _ := renderNodes(u.Nodes, width, 0, 0, renderSettings{verbose: true})
		fmt.Println(strings.Join(rows, "\n"))
		fmt.Println()
	}
}

func mustAtoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		die("show: want an integer, got %q", s)
	}
	return v
}

// selectUnitRange resolves the [lo,hi) unit window from the flags:
// --all = everything; --from/--to = an inclusive index range; otherwise
// the last N (default 10).
func selectUnitRange(total int, o showOpts) (int, int) {
	if o.all {
		return 0, total
	}
	if o.from >= 0 || o.to >= 0 {
		lo := 0
		if o.from > 0 {
			lo = o.from
		}
		hi := total
		if o.to >= 0 && o.to+1 < total {
			hi = o.to + 1
		}
		if lo > total {
			lo = total
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
// verbose mode, appends credo / state transitions / chalkboard.
func renderAriaIR(loaded *config.Loaded, figaroID string, entries []store.Entry[message.Message], opts showOpts) {
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

	fmt.Fprintf(w, "# aria %s — showing %d of %d messages\n\n", figaroID, len(entries)-start, len(entries))
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
	// The chalkboard is a reducible channel now (there is no chalkboard.json);
	// fetch the live snapshot through the angelus. Best-effort: the credo and
	// chalkboard sections degrade to empty rather than aborting the dump.
	snap := fetchChalkboardSnapshot(loaded, figaroID)
	if raw, ok := snap["system.credo"]; ok {
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
		fmt.Println("(no system.credo on chalkboard)")
	}
	fmt.Println()
	fmt.Println("---")
	fmt.Println("## state transitions")
	fmt.Println()
	printTransitions(os.Stdout, entries)
	fmt.Println()
	fmt.Println("---")
	fmt.Println("## chalkboard")
	fmt.Println()
	printSnapshot(os.Stdout, snap)
}

// printTransitions prints all chalkboard patches in LT order.
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
	case message.RoleUser:
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
			fmt.Fprintf(w, "**you** [#%d]\n\n> %s\n\n", lt, indentBlockquote(text))
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

	case message.RoleAssistant:
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
