package cli

// Every candidate source answers in the completion protocol's shape,
// "value<TAB>description"; shells get the bare values, fish and the pager's Tab
// pit the lot.
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
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/sdk"
)

func described(c *cmdkit.CompleteContext, value, desc string) string {
	if c == nil || !c.Describe {
		return value
	}
	return cmdkit.Candidate(value, desc)
}

// completionSubject, when set, is the aria completion reads a form from in
// place of the shell's binding.
var completionSubject string

func ariaCandidates(c *cmdkit.CompleteContext) []string { return ariasOnly(idCandidates(c)) }

// The described form costs the full listing; the bare one stays on the cheap
// id-only listing.
func idCandidates(c *cmdkit.CompleteContext) []string {
	if c == nil || !c.Describe {
		return softFetchAriaIDs()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(doorEndpoint())
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
	// Most recent first.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at > rows[j].at })
	out := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		out = append(out, cmdkit.Candidate(r.id, r.desc))
		seen[r.id] = true
	}
	// The full listing leaves out unbound forms; whatever the id listing has
	// that this one lacks goes on the end.
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

// A leading ~ is expanded to read and kept in what is offered; dotfiles appear
// only once the dot is typed.
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

func looksLikePath(w string) bool {
	return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~") ||
		strings.HasPrefix(w, "./") || strings.HasPrefix(w, "../")
}

func completeFileArg(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	return pathCandidatesFor(c, c.Current, false)
}

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
			return nil
		}
	}
	return ariaCandidates(c)
}

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

func skillDescription(frontmatter string) string {
	for _, line := range strings.Split(frontmatter, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "description:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func valueSummary(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.Join(strings.Fields(s), " ")
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

func completeTroupeAfterLogin(c *cmdkit.CompleteContext) []string {
	if c == nil || len(c.Args) != 1 || c.Args[0] != "login" {
		return nil
	}
	loaded, err := config.Load(config.DefaultConfigDir())
	if err != nil {
		return nil
	}
	names, _ := boot.TroupeNames(loaded)
	return names
}

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

// Forms and arias share one listing, and a form offered where an aria is wanted
// is silently accepted, so each slot asks for its kind.
func isFormID(id string) bool { return strings.HasPrefix(id, "@") }

func ariasOnly(cands []string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if v, _ := cmdkit.SplitCandidate(c); !isFormID(v) {
			out = append(out, c)
		}
	}
	return out
}

func formsOnly(cands []string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if v, _ := cmdkit.SplitCandidate(c); isFormID(v) {
			out = append(out, c)
		}
	}
	return out
}

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
					i++
					break
				}
			}
			continue
		}
		n++
	}
	return n
}

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

func completeTarget(c *cmdkit.CompleteContext) []string {
	id, _, hasColon := strings.Cut(c.Current, ":")
	if !hasColon || isFormID(id) {
		return ariaCandidates(c)
	}
	return turnCandidates(c, id)
}

const turnCandidatesMax = 200

func turnCandidates(c *cmdkit.CompleteContext, id string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	acli, err := sdk.DialAngelus(doorEndpoint())
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
				continue
			}
		}
		out = append(out, described(c, v, p.Inquiry))
	}
	return out
}

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
