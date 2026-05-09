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
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/store"
)

// runAria handles the unified aria verb. Two shapes:
//
//	figaro aria [id] [N] [-v] [-l] [-a]            → render history
//	figaro aria [id] -- <prompt>                    → prompt the aria
//
// Without an id, the pid-bound aria is used. With an id that doesn't
// exist, a new aria is created with that id (no pid binding). With an
// id that does exist (live or dormant), it is dialed (the angelus
// restores dormant arias on demand). Render mode is selected when no
// `--` is present in args; prompt mode when `--` is present.
func runAria(loaded *config.Loaded, args []string) {
	// Split args at the first `--`. Everything before is selectors
	// (id, N, flags); everything after is the prompt body.
	var head, tail []string
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	if dashIdx >= 0 {
		head = args[:dashIdx]
		tail = args[dashIdx+1:]
	} else {
		head = args
	}

	// Pull a leading id if present. An id is the first head arg that
	// validates as an aria id, isn't numeric (back-compat: bare N
	// still means N), and isn't a flag.
	var id string
	if len(head) > 0 {
		candidate := head[0]
		if _, err := strconv.Atoi(candidate); err != nil &&
			!strings.HasPrefix(candidate, "-") &&
			rpc.ValidateAriaID(candidate) == nil {
			id = candidate
			head = head[1:]
		}
	}

	if dashIdx >= 0 {
		prompt := strings.Join(tail, " ")
		if prompt == "" {
			die("usage: figaro aria [<id>] -- <prompt>")
		}
		promptAria(loaded, id, prompt)
		return
	}

	renderAria(loaded, id, head)
}

// renderAria parses the render-mode flags and prints history. If id
// is empty, the pid-bound aria is used.
func renderAria(loaded *config.Loaded, id string, args []string) {
	n := 10
	verbose := false
	literal := false
	all := false
	// Expand bundled short flags: -alv → -a -l -v.
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
				die("usage: figaro aria [<id>] [N] [-v|--verbose] [-l|--literal] [-a|--all]")
			}
			n = parsed
		}
	}

	figaroID := id
	if figaroID == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		acli := mustConnectAngelus(loaded)
		defer acli.Close()
		r, err := acli.Resolve(ctx, os.Getppid())
		if err != nil {
			die("resolve: %s", err)
		}
		if !r.Found {
			die("no figaro bound to this shell")
		}
		figaroID = r.FigaroID
	}

	home, _ := os.UserHomeDir()
	ariaPath := filepath.Join(home, ".local", "state", "figaro", "arias", figaroID, "aria.jsonl")
	fs, err := store.OpenFileStream[message.Message](ariaPath)
	if err != nil {
		die("open aria: %s", err)
	}
	entries := fs.Read()
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "(empty aria)")
		return
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
		fmt.Println("## system prompt")
		fmt.Println()
		cbPath := filepath.Join(home, ".local", "state", "figaro", "arias", figaroID, "chalkboard.json")
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
		if raw, ok := snap["system.prompt"]; ok {
			var sp string
			if err := json.Unmarshal(raw, &sp); err == nil {
				fmt.Println(sp)
			} else {
				fmt.Println(string(raw))
			}
		} else {
			fmt.Println("(no system.prompt on chalkboard)")
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

// printTransitions walks the aria stream and prints every patch in
// LT order, so a verbose `figaro aria` reader can see the full
// timeline of chalkboard mutations that produced the current state.
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
			case message.ContentToolCall:
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
