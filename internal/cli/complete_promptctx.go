package cli

import (
	"os"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// completePromptContext is the candidate pool for the cursor when it
// has passed the `--` separator in `figaro <verb> [flags] -- <body>`.
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
		// A word that names a directory completes inside it; listCWD alone
		// only ever reached one level.
		out = append(out, pathCandidatesFor(c, c.Current, false)...)
	} else {
		out = append(out, listCWD()...)
	}
	sort.Strings(out)
	return out
}

// listCWD returns the names of entries in the current working
// directory, with a trailing "/" on directories. Hidden entries and
// names containing shell-unsafe characters are filtered out (see
// completePromptContext for the why).
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

// containsShellUnsafe reports whether s contains a character that
// would break round-tripping through compgen -W in the generated
// bash/zsh completion scripts. The list is conservative: anything
// that would word-split, glob, or otherwise be reinterpreted.
func containsShellUnsafe(s string) bool {
	const bad = " \t\n\"'`$\\*?[]|&;<>()!{}"
	return strings.ContainsAny(s, bad)
}

// completePromptOrIDFlag is the prompt-command completer: aria ids
// after --id, and the prompt-context pool past `--`. Falls through
// to nil otherwise. Used by send/plain/x/new.
func completePromptOrIDFlag(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	// PAST THE BOUNDARY FIRST: after a user-typed "--", "--id" and "-O" are
	// words in a prompt, not flags. Checking them first completed a prompt
	// body as a flag value.
	if c.PastSeparator {
		return completePromptContext(c)
	}
	// --id <here>: aria ids win over everything else; the cursor is
	// unambiguously typing a flag value.
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return ariaCandidates(c)
	}
	if c.PastSeparator {
		return completePromptContext(c)
	}
	return nil
}

// completeOutfitFlag offers outfit names when the cursor sits after -O. Every
// prompt verb takes the flag, so every prompt verb consults this first.
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

// completeNewPrompt is completePromptOrIDFlag plus outfit names after -O.
// Used by `new` and by `send`.
func completeNewPrompt(c *cmdkit.CompleteContext) []string {
	if c == nil {
		return nil
	}
	if out := completeOutfitFlag(c); out != nil {
		return out
	}
	return completePromptOrIDFlag(c)
}

// completeForkPrompt serves `figaro fork`: aria ids in the target slot
// (positional or after --id), prompt context past the `--` boundary.
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
	// The fork point is a target: an aria, and past its colon a turn.
	if positionals(c.Args, "--id", "-O", "--outfit") == 0 {
		return completeTarget(c)
	}
	return nil
}
