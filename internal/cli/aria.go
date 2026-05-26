package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/largo"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// runShow handles `figaro show [--id <id>] [N] [-v|-l|-a]`.
func runShow(loaded *config.Loaded, idFlag string, args []string) {
	renderAria(loaded, idFlag, args)
}

// renderAria prints history for an aria.
func renderAria(loaded *config.Loaded, id string, args []string) {
	n := 10
	verbose := false
	literal := false
	all := false
	// Expand bundled short flags.
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
	for _, arg := range expanded {
		switch arg {
		case "-v", "--verbose":
			verbose = true
		case "-l", "--literal":
			literal = true
		case "-a", "--all":
			all = true
		default:
			parsed, err := strconv.Atoi(arg)
			if err != nil {
				die("usage: figaro show [--id <id>] [N] [-v|--verbose] [-l|--literal] [-a|--all]")
			}
			n = parsed
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

	// Read through the angelus's shared LogCache. The angelus is the
	// single owner of the figwal log so we don't race the live agent
	// on the active segment.
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
	start := 0
	if !all && len(entries) > n {
		start = len(entries) - n
	}

	var w io.Writer = os.Stdout
	flush := func() {}
	if !literal {
		sw, err := largo.NewWriter(os.Stdout, largo.Options{})
		if err != nil {
			die("largo: %s", err)
		}
		w = sw
		flush = func() { sw.Flush() }
	}

	fmt.Fprintf(w, "# aria %s — showing %d of %d messages\n\n", figaroID, len(entries)-start, len(entries))
	for _, e := range entries[start:] {
		renderMessage(w, e.Payload, e.LT, verbose)
	}
	flush()

	if verbose {
		fmt.Println()
		fmt.Println("---")
		fmt.Println("## credo")
		fmt.Println()
		cbPath := filepath.Join(stateDir(), "arias", figaroID, "chalkboard.json")
		cbData, err := os.ReadFile(cbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read chalkboard: %s\n", err)
			return
		}
		var snap map[string]json.RawMessage
		if err := json.Unmarshal(cbData, &snap); err != nil {
			fmt.Fprintf(os.Stderr, "parse chalkboard: %s\n", err)
			return
		}
		if raw, ok := snap["system.credo"]; ok {
			// system.credo may be a bare string or a ContentEnvelope
			// object ({content, frontmatter, filePath}). Prefer content,
			// fall back to frontmatter, then to the raw string.
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
			case message.ContentText:
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
			case message.ContentText:
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
