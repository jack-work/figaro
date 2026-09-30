package cli

// THE CANDIDATE SOURCES. Every one of them answers in the completion
// protocol's shape, "value<TAB>description" (cmdkit.Candidate), and every
// caller shares them: bash and zsh get the bare values (the dispatcher strips
// descriptions unless asked), fish and the pager's Tab pit get the lot.
//
// ONE IMPLEMENTATION, TWO FACES. The shells draw their own menus. The pager
// has none, so it draws one (transcript_complete.go) over exactly these
// candidates, reached through the same `__complete` dispatcher a shell calls.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

// described is a candidate with its description when the caller asked for
// descriptions, and the bare value otherwise. The dispatcher would strip them
// anyway; this keeps a direct caller (a test, completePromptContext's sort)
// looking at values.
func described(c *cmdkit.CompleteContext, value, desc string) string {
	if c == nil || !c.Describe {
		return value
	}
	return cmdkit.Candidate(value, desc)
}

// completionSubject, when set, is the aria completion reads a form from in
// place of the shell's binding. The pager sets it for the length of one
// completion: its subject is the aria on screen, which the shell may not be
// attending at all (`figaro listen X` binds nothing).
var completionSubject string

// ariaCandidates is every ARIA id: idCandidates without the unbound forms,
// which are offered only where a verb takes one (completeFormVerb).
func ariaCandidates(c *cmdkit.CompleteContext) []string { return ariasOnly(idCandidates(c)) }

// idCandidates is every id the daemon lists, arias and forms, described by
// its mantra when the caller asked for descriptions. The described form costs the full listing (tens of
// milliseconds against a thousand arias); the bare one stays on the cheap
// id-only listing a shell's Tab has always used.
func idCandidates(c *cmdkit.CompleteContext) []string {
	if c == nil || !c.Describe {
		return softFetchAriaIDs()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return nil
	}
	defer acli.Close()
	resp, err := acli.List(ctx)
	if err != nil {
		return nil
	}
	type row struct {
		id, desc string
		at       int64
	}
	rows := make([]row, 0, len(resp.Figaros))
	for _, f := range resp.Figaros {
		if f.ID == "" {
			continue
		}
		// "idle · mantra", or whichever half exists: an aria with no mantra
		// yet was described as "idle ·".
		var parts []string
		if f.State != "" && f.State != "dormant" {
			parts = append(parts, f.State)
		}
		if m := strings.TrimSpace(f.Mantra); m != "" {
			parts = append(parts, m)
		}
		desc := strings.Join(parts, " · ")
		rows = append(rows, row{f.ID, desc, f.LastActive})
	}
	// MOST RECENT FIRST. A menu is read top-down, and the aria you want is
	// almost always one you touched lately; alphabetical order put a
	// thousand hex ids between you and it.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at > rows[j].at })
	out := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		out = append(out, cmdkit.Candidate(r.id, r.desc))
		seen[r.id] = true
	}
	// THE FULL LISTING LEAVES OUT UNBOUND FORMS, which the id-only one has:
	// `study @<TAB>` offered nothing in the pit while bash, on the cheap
	// listing, offered the form. Whatever the id listing has that this one
	// lacks goes on the end.
	if ids, err := acli.ListIDs(ctx); err == nil {
		for _, f := range ids.Figaros {
			if f.ID != "" && !seen[f.ID] {
				desc := "aria"
				if isFormID(f.ID) {
					desc = "form"
				}
				out = append(out, cmdkit.Candidate(f.ID, desc))
			}
		}
	}
	return out
}

// pathCandidates completes a filesystem path the way a shell does: the
// entries of the directory the partial names, filtered by the partial's last
// segment, directories with a trailing slash. A leading ~ is expanded to read
// and KEPT in what is offered, so the line still says what the user typed.
// Dotfiles appear only once the user has typed the dot.
func pathCandidates(current string, dirsOnly bool) []string {
	return pathCandidatesFor(nil, current, dirsOnly)
}

func pathCandidatesFor(c *cmdkit.CompleteContext, current string, dirsOnly bool) []string {
	dir, prefix := splitPath(current)
	read := dir
	switch {
	case read == "":
		read = "."
	case read == "~/" || strings.HasPrefix(read, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		read = filepath.Join(home, strings.TrimPrefix(read, "~/"))
	}
	entries, err := os.ReadDir(read)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(read, name)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if dirsOnly && !isDir {
			continue
		}
		if isDir {
			out = append(out, described(c, dir+name+"/", "dir"))
			continue
		}
		out = append(out, described(c, dir+name, "file"))
	}
	sort.Strings(out)
	return out
}

// splitPath is filepath.Split that treats a bare "~" as the home directory,
// so "~" completes to "~/" rather than to entries named "~...".
func splitPath(p string) (dir, base string) {
	if p == "~" {
		return "~/", ""
	}
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", p
	}
	return p[:i+1], p[i+1:]
}

// looksLikePath is a word a shell would complete as a path whatever the
// command: it names a directory explicitly.
func looksLikePath(w string) bool {
	return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~") ||
		strings.HasPrefix(w, "./") || strings.HasPrefix(w, "../")
}

// completeFileArg is a verb whose positional is a file: the path under the
// cursor, and ids after --id where the verb has one.
func completeFileArg(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	return pathCandidatesFor(c, c.Current, false)
}

// completeExport is `export [<id>] [-o <file>]`: a path after -o, an aria id
// in the positional slot.
func completeExport(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if n := len(c.Args); n > 0 {
		switch c.Args[n-1] {
		case "-o", "--output", "--out":
			return pathCandidatesFor(c, c.Current, false)
		case "--id":
			return ariaCandidates(c)
		}
	}
	for _, a := range c.Args {
		if !strings.HasPrefix(a, "-") {
			return nil // the aria is already named
		}
	}
	return ariaCandidates(c)
}

// completeWords offers a fixed sub-verb vocabulary in the first positional
// slot, described, then defers to rest (which may be nil).
func completeWords(words [][2]string, rest func(*cmdkit.CompleteContext) []string) func(*cmdkit.CompleteContext) []string {
	return func(c *cmdkit.CompleteContext) []string {
		if c == nil {
			return nil
		}
		if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
			return ariaCandidates(c)
		}
		positional := 0
		for _, a := range c.Args {
			if !strings.HasPrefix(a, "-") {
				positional++
			}
		}
		if positional == 0 {
			out := make([]string, len(words))
			for i, w := range words {
				out[i] = described(c, w[0], w[1])
			}
			return out
		}
		if rest != nil {
			return rest(c)
		}
		return nil
	}
}

// skillDescription is the description line of a skill's frontmatter, which is
// what a skill says about itself: the one sentence that tells a reader
// whether it is the one they meant.
func skillDescription(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// valueSummary renders a board value for a description column: a string as
// itself, anything else as compact JSON, on one line either way.
func valueSummary(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.Join(strings.Fields(s), " ")
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

// completeProviders is `login <provider>`: the registered providers, the
// ones that can log in first.
func completeProviders(c *cmdkit.CompleteContext) []string {
	if c == nil || len(c.Args) > 0 {
		return nil
	}
	var out []string
	for _, name := range provider.Names() {
		desc := ""
		if reg := provider.Lookup(name); reg != nil {
			desc = reg.LoginHint
		}
		out = append(out, described(c, name, desc))
	}
	return out
}

// isFormID is an unbound form's id: the @ sigil. Forms and arias share one
// listing, and a form offered where an aria is wanted is silently accepted
// (`figaro show @62b4222c` answers "(empty aria)"), so each slot asks for the
// kind it takes.
func isFormID(id string) bool { return strings.HasPrefix(id, "@") }

// ariasOnly drops the forms from an id pool, keeping descriptions.
func ariasOnly(cands []string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if v, _ := cmdkit.SplitCandidate(c); !isFormID(v) {
			out = append(out, c)
		}
	}
	return out
}

// formsOnly is the other half.
func formsOnly(cands []string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if v, _ := cmdkit.SplitCandidate(c); isFormID(v) {
			out = append(out, c)
		}
	}
	return out
}

// positionals counts the words before the cursor that are not flags or the
// values of the flags named in valued.
func positionals(args []string, valued ...string) int {
	n := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			for _, v := range valued {
				if a == v && !strings.Contains(a, "=") {
					i++ // its value is not a positional
					break
				}
			}
			continue
		}
		n++
	}
	return n
}

// completeFormVerb is study, drop, cast and bind: `[<aria>] <@form>`. The
// first slot may be either, so it offers both; the second takes a form.
func completeFormVerb(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if n := len(c.Args); n > 0 && c.Args[n-1] == "--id" {
		return ariaCandidates(c)
	}
	switch positionals(c.Args, "--id", "-O", "--outfit", "-S", "--set", "--delete") {
	case 0:
		return idCandidates(c)
	case 1:
		return formsOnly(idCandidates(c))
	}
	return nil
}

// completeSend is `send [<aria>[:<turn>[.<node>]]] [flags] -- <prompt>`: the
// target before the boundary, the prompt context past it. It offered only the
// prompt context, so a shell showed files where an aria id belongs.
func completeSend(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if out := completeOutfitFlag(c); out != nil {
		return out
	}
	if out := completePromptOrIDFlag(c); out != nil || c.PastSeparator {
		return out
	}
	if positionals(c.Args, "--id", "-O", "--outfit") == 0 {
		return completeTarget(c)
	}
	return nil
}

// completeTarget is an aria that may carry a coordinate: attend, fork, send.
// Past the colon it offers the aria's turns, each described by the question
// that opened it, which is how a person recognises a turn.
func completeTarget(c *cmdkit.CompleteContext) []string {
	id, _, hasColon := strings.Cut(c.Current, ":")
	if !hasColon || isFormID(id) {
		return ariaCandidates(c)
	}
	return turnCandidates(c, id)
}

// turnCandidatesMax bounds a coordinate completion: the newest turns first,
// and no more than a menu can usefully show.
const turnCandidatesMax = 200

// turnCandidates is `<id>:<turn>` for the aria's turns, newest first.
func turnCandidates(c *cmdkit.CompleteContext, id string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(angelusSocketPath()))
	if err != nil {
		return nil
	}
	defer acli.Close()
	page, err := acli.Read(ctx, rpc.ReadRequest{FigaroID: id, Backward: true})
	if err != nil {
		return nil
	}
	var out []string
	for i := len(page.Parts) - 1; i >= 0 && len(out) < turnCandidatesMax; i-- {
		p := page.Parts[i]
		v := id + ":" + strconv.FormatUint(p.ID, 10)
		if len(out) > 0 {
			if prev, _ := cmdkit.SplitCandidate(out[len(out)-1]); prev == v {
				continue // one turn can span two parts of a page
			}
		}
		out = append(out, described(c, v, p.Inquiry))
	}
	return out
}

// completeSetArgs is `set <key> <value>`: keys in the key slot, and nothing
// in the value slot. It re-offered the key catalog there.
func completeSetArgs(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if n := len(c.Args); n > 0 && c.Args[n-1] == "--id" {
		return ariaCandidates(c)
	}
	if positionals(c.Args, "--id") > 0 {
		return nil
	}
	return completeFormKeys(c)
}

// completeAttend is `attend <aria>[:<turn>[.<node>]]` or `attend null`.
func completeAttend(c *cmdkit.CompleteContext) []string {
	if c == nil || len(c.Args) > 0 {
		return nil
	}
	out := completeTarget(c)
	if !strings.Contains(c.Current, ":") {
		out = append(out, described(c, "null", "attend nothing: unbind this shell"))
	}
	return out
}
