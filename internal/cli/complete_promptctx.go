package cli

import (
	"os"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// completePromptContext is the candidate pool for the cursor when it
// has passed the `--` separator in `figaro <verb> [flags] -- <body>`.
//
// When the cursor's current partial token (ctx.Current) starts with
// "@", the candidate pool narrows to chalkboard-key references:
// every key is emitted as "@<key>!" so the accepted candidate is
// a complete reference (terminator included). expandAtRefs requires
// the trailing "!" to substitute, so emitting it here means the
// user doesn't have to type it manually.
//
// Otherwise the pool is the union of:
//
//   - chalkboard keys (well-known + live snapshot, via the existing
//     completeChalkboardKeys plumbing — same source the `set` and
//     `unset` commands use).
//   - entries from the current working directory, with a trailing "/"
//     on directories so the shell renders them correctly.
//
// Entries with whitespace or shell-special characters are skipped:
// the bash/zsh completion scripts feed candidates through compgen -W
// which word-splits on IFS, mangling such names. A future pass can
// rework the shell-side glue to handle quoting; for now we degrade
// cleanly rather than emit broken candidates.
//
// Hidden entries (leading ".") are skipped: without knowing the
// cursor's current prefix we'd pollute the suggestion list with
// every dotfile.
func completePromptContext(c *cmdkit.CompleteContext) []string {
	if c != nil && strings.HasPrefix(c.Current, "@") {
		keys := completeChalkboardKeys(nil)
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = "@" + k + "!"
		}
		return out
	}
	out := completeChalkboardKeys(nil)
	out = append(out, listCWD()...)
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
	// --id <here>: aria ids win over everything else; the cursor is
	// unambiguously typing a flag value.
	if len(c.Args) > 0 && c.Args[len(c.Args)-1] == "--id" {
		return softFetchAriaIDs()
	}
	if c.PastSeparator {
		return completePromptContext(c)
	}
	return nil
}
