package cli

import (
	"os"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

func completePromptContext(c *cmdkit.CompleteContext) []string {
	sigil := string(refSigil)
	if c != nil && strings.HasPrefix(c.Current, sigil) {
		keys := completeFormKeys(c)
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = sigil + k
		}
		return out
	}
	out := completeFormKeys(c)
	if c != nil && (strings.Contains(c.Current, "/") || strings.HasPrefix(c.Current, "~")) {
		out = append(out, pathCandidatesFor(c, c.Current, false)...)
	} else {
		out = append(out, listCWD()...)
	}
	sort.Strings(out)
	return out
}

// listCWD filters hidden entries and names with shell-unsafe characters.
func listCWD() []string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if containsShellUnsafe(name) {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

// containsShellUnsafe: anything that would break round-tripping through compgen
// -W in the bash/zsh scripts.
func containsShellUnsafe(s string) bool {
	const bad = " \t\n\"'`$\\*?[]|&;<>()!{}"
	return strings.ContainsAny(s, bad)
}

func completePromptOrIDFlag(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	// Past the boundary first: after a typed "--", "--id" and "-O" are words
	// in a prompt.
	if c.PastSeparator {
		return completePromptContext(c)
	}
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	if c.PastSeparator {
		return completePromptContext(c)
	}
	return nil
}

func completeOutfitFlag(c *cmdkit.CompleteContext) []string {
	if len(c.Args) == 0 || c.PastSeparator {
		return nil
	}
	switch c.Args[len(c.Args)-1] {
	case "--outfit", "-O":
		return completeOutfits(c)
	}
	return nil
}

func completeNewPrompt(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if out := completeOutfitFlag(c); out != nil {
		return out
	}
	return completePromptOrIDFlag(c)
}

func completeForkPrompt(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if out := completeOutfitFlag(c); out != nil {
		return out
	}
	if c.PastSeparator {
		return completePromptContext(c)
	}
	if n := len(c.Args); n > 0 && c.Args[n-1] == "--id" {
		return ariaCandidates(c)
	}
	if positionals(c.Args, "--id", "-O", "--outfit") == 0 {
		return completeTarget(c)
	}
	return nil
}
