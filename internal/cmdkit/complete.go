package cmdkit

import (
	"fmt"
	"io"
	"strings"
)

// completeVerb is the hidden subcommand the generated completion
// scripts shell out to for dynamic candidates.
const completeVerb = "__complete"

// barePromptSentinel is the verb-position token the shell-side
// completion scripts substitute when the user is in the bare-prompt
// form (`figaro -- <body>` or any alias of it, e.g. `q ` expanding
// to `figaro -- `). The dispatcher recognizes it and routes to the
// bare-prompt completer registered via SetBarePromptComplete.
const barePromptSentinel = "__bare_prompt"

// currentFlag is the optional flag the shell-side scripts use to
// pass the cursor's current partial token. Wire format:
//
//	__complete <verb> [--current <cur>] -- <tokens before cursor>
//
// Old scripts that omit it still work; the dispatcher leaves
// CompleteContext.Current empty in that case.
const currentFlag = "--current"

// CompletionShell identifies a shell for completion script generation.
type CompletionShell string

const (
	ShellBash CompletionShell = "bash"
	ShellZsh  CompletionShell = "zsh"
	ShellFish CompletionShell = "fish"
)

// SetBarePromptComplete registers a CompleteArgs callback that fires
// when the user is typing in the bare-prompt form: `figaro -- <body>`
// (or an alias such as `q ` that expands to that). The cursor lives
// past the `--`, so callbacks should behave as if PastSeparator is
// true. Callers typically wire this to completePromptContext or its
// composition.
func (r *Router) SetBarePromptComplete(fn func(*CompleteContext) []string) {
	r.barePromptComplete = fn
}

// runComplete is the hidden __complete dispatcher. Args layout:
//
//	__complete <verb> [--current <cur>] -- <tokens before cursor>
//
// Prints one candidate per line on stdout. Unknown verb or missing
// callback exits silently with code 0 so completion never appears
// broken to the user.
func (r *Router) runComplete(ctx *RunContext) error {
	raw := ctx.RawArgs
	// The router may have left a leading "--" boundary marker from
	// PassRaw. There is at most one such marker; strip exactly one,
	// not more, so a user-typed "--" sitting in second position
	// (the prompt-body separator in `verb -- <body>`) survives.
	if len(raw) > 0 && raw[0] == "--" {
		raw = raw[1:]
	}
	if len(raw) == 0 {
		return nil
	}
	verb := raw[0]
	tail := raw[1:]
	// Optional --current <cur> immediately after the verb.
	var current string
	if len(tail) >= 2 && tail[0] == currentFlag {
		current = tail[1]
		tail = tail[2:]
	}
	// Same logic for the boundary marker between verb/flags and
	// tokens: the shell-side completion scripts insert exactly one
	// "--" here. Strip one, never more.
	if len(tail) > 0 && tail[0] == "--" {
		tail = tail[1:]
	}
	// Any "--" that survives is one the user typed themselves (the
	// conventional flags/prompt separator). Detect it and surface it
	// through CompleteContext so callbacks can switch candidate pools
	// when the cursor lives past it.
	pastSep := false
	for _, tok := range tail {
		if tok == "--" {
			pastSep = true
			break
		}
	}

	// Resolve which CompleteArgs to call: bare-prompt sentinel goes
	// to the dedicated callback (cursor is conceptually past --);
	// every other verb goes through the command registry.
	var fn func(*CompleteContext) []string
	if verb == barePromptSentinel {
		fn = r.barePromptComplete
		// The bare-prompt path is *always* past-separator from the
		// callback's perspective: the user has already invoked the
		// program with a "--" boundary (or an alias of it).
		pastSep = true
	} else {
		cmd, ok := r.index[verb]
		if !ok {
			return nil
		}
		fn = cmd.CompleteArgs
	}
	if fn == nil {
		return nil
	}

	cands := fn(&CompleteContext{
		Args:          tail,
		Current:       current,
		PastSeparator: pastSep,
		Extra:         r.Extra,
	})
	for _, c := range cands {
		fmt.Println(c)
	}
	return nil
}

// WriteCompletion generates a shell completion script and writes it
// to w. Returns an error for unsupported shells.
func (r *Router) WriteCompletion(w io.Writer, shell CompletionShell) error {
	switch shell {
	case ShellBash:
		return r.writeBashCompletion(w)
	case ShellZsh:
		return r.writeZshCompletion(w)
	case ShellFish:
		return r.writeFishCompletion(w)
	default:
		return fmt.Errorf("unsupported shell: %q (use bash, zsh, or fish)", shell)
	}
}

func (r *Router) writeBashCompletion(w io.Writer) error {
	cmds := r.visibleCommandNames()
	// Bash logic:
	//   - At word 1: emit the verb list (or, if word 1 is "--", route
	//     to the bare-prompt completer instead).
	//   - Beyond:    resolve the verb. If verb == "--", route to the
	//     bare-prompt completer (cursor lives past --). Otherwise
	//     dispatch by verb name.
	//
	// The cursor's current partial token (${COMP_WORDS[COMP_CWORD]})
	// is passed via --current so callbacks can switch pools based on
	// a sigil prefix like "@".
	fmt.Fprintf(w, `# bash completion for %s
_%s_completions() {
    COMPREPLY=()
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local verb="${COMP_WORDS[1]}"
    local sentinel="%s"
    if [ "$COMP_CWORD" -eq 1 ]; then
        local commands="%s"
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi
    if [ "$verb" = "--" ]; then
        verb="$sentinel"
    fi
    local count=$((COMP_CWORD - 2))
    local args=()
    if [ "$count" -gt 0 ]; then
        args=("${COMP_WORDS[@]:2:$count}")
    fi
    local candidates
    candidates=$(%s %s "$verb" --current "$cur" -- "${args[@]}" 2>/dev/null)
    if [ -n "$candidates" ]; then
        COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
    fi
}
complete -F _%s_completions %s
`, r.Name, r.Name, barePromptSentinel, strings.Join(cmds, " "), r.Name, completeVerb, r.Name, r.Name)
	return nil
}

func (r *Router) writeZshCompletion(w io.Writer) error {
	fmt.Fprintf(w, `#compdef %s

__%s_commands() {
    local -a commands
    commands=(
`, r.Name, r.Name)
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		desc := strings.ReplaceAll(cmd.Short, "'", "'\\''")
		fmt.Fprintf(w, "        '%s:%s'\n", cmd.Name, desc)
	}
	fmt.Fprintf(w, `    )
    _describe 'command' commands
}

__%s_dynamic() {
    local verb=$words[2]
    local sentinel="%s"
    if [[ $verb == "--" ]]; then
        verb=$sentinel
    fi
    local cur=$words[CURRENT]
    local -a args
    if (( CURRENT > 3 )); then
        args=( "${words[@]:2:$((CURRENT - 3))}" )
    fi
    local -a candidates
    candidates=( ${(f)"$(%s %s $verb --current $cur -- $args 2>/dev/null)"} )
    if (( ${#candidates} )); then
        compadd -- $candidates
    fi
}

_%s() {
    if (( CURRENT == 2 )); then
        __%s_commands
    else
        __%s_dynamic
    fi
}

_%s "$@"
`, r.Name, r.Name, barePromptSentinel, completeVerb, r.Name, r.Name, r.Name, r.Name)
	return nil
}

func (r *Router) writeFishCompletion(w io.Writer) error {
	// Note: we do NOT call `complete -c <name> -f` to disable fish's
	// native filename fallback. The __complete dispatcher emits a
	// best-effort candidate pool (e.g. chalkboard keys + a filtered
	// CWD listing past --), but it deliberately drops names with
	// shell-unsafe characters and hidden entries. Fish's built-in
	// file completion handles those cases natively and is far more
	// polished than anything we'd reinvent — so we let it ride
	// alongside our dynamic candidates.
	//
	// The dynamic function detects the bare-prompt form (`figaro --`
	// or an alias expanding to it) and substitutes the sentinel verb
	// so the dispatcher routes to the bare-prompt completer. The
	// cursor's current partial token (commandline -ct) is passed via
	// --current.
	fmt.Fprintf(w, `# fish completion for %s
function __%s_dynamic
    set -l tokens (commandline -opc)
    if test (count $tokens) -lt 2
        return
    end
    set -l verb $tokens[2]
    set -l sentinel %s
    if test "$verb" = "--"
        set verb $sentinel
    end
    set -l cur (commandline -ct)
    set -l args
    if test (count $tokens) -gt 2
        set args $tokens[3..-1]
    end
    %s %s $verb --current "$cur" -- $args 2>/dev/null
end

# Bare-prompt detector: when the first token after the program name
# is "--" (or an alias has placed us in that form), suggest from the
# dynamic pool — not the subcommand list.
function __%s_is_bare_prompt
    set -l tokens (commandline -opc)
    test (count $tokens) -ge 2; and test $tokens[2] = "--"
end
`, r.Name, r.Name, barePromptSentinel, r.Name, completeVerb, r.Name)
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		desc := strings.ReplaceAll(cmd.Short, "'", "\\'")
		// Subcommand suggestions: only when fish thinks we are at
		// the subcommand position AND we're not in the bare-prompt
		// form (so `q <TAB>` doesn't get a verb list).
		fmt.Fprintf(w, "complete -c %s -n '__fish_use_subcommand; and not __%s_is_bare_prompt' -a %s -d '%s'\n",
			r.Name, r.Name, cmd.Name, desc)
	}
	fmt.Fprintf(w, "complete -c %s -n 'not __fish_use_subcommand' -a '(__%s_dynamic)'\n",
		r.Name, r.Name)
	// Also surface dynamic candidates at the subcommand position
	// when we're in the bare-prompt form (the negative condition
	// above suppresses verbs in that case).
	fmt.Fprintf(w, "complete -c %s -n '__fish_use_subcommand; and __%s_is_bare_prompt' -a '(__%s_dynamic)'\n",
		r.Name, r.Name, r.Name)
	return nil
}

func (r *Router) visibleCommandNames() []string {
	var names []string
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		names = append(names, cmd.Name)
		names = append(names, cmd.Aliases...)
	}
	return names
}
