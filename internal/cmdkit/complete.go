package cmdkit

import (
	"fmt"
	"io"
	"strings"
)

// completeVerb is the hidden subcommand the generated completion
// scripts shell out to for dynamic candidates.
const completeVerb = "__complete"

// CompletionShell identifies a shell for completion script generation.
type CompletionShell string

const (
	ShellBash CompletionShell = "bash"
	ShellZsh  CompletionShell = "zsh"
	ShellFish CompletionShell = "fish"
)

// runComplete is the hidden __complete dispatcher. Args layout:
//
//	__complete <verb> -- <tokens before cursor>
//
// Prints one candidate per line on stdout. Unknown verb or missing
// callback exits silently with code 0 so completion never appears
// broken to the user.
func (r *Router) runComplete(ctx *RunContext) error {
	raw := ctx.RawArgs
	// Strip leading "--" if present (router doesn't for PassRaw).
	for len(raw) > 0 && raw[0] == "--" {
		raw = raw[1:]
	}
	if len(raw) == 0 {
		return nil
	}
	verb := raw[0]
	cmd, ok := r.index[verb]
	if !ok || cmd.CompleteArgs == nil {
		return nil
	}
	tail := raw[1:]
	for len(tail) > 0 && tail[0] == "--" {
		tail = tail[1:]
	}
	cands := cmd.CompleteArgs(&CompleteContext{
		Args:  tail,
		Extra: r.Extra,
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
	fmt.Fprintf(w, `# bash completion for %s
_%s_completions() {
    COMPREPLY=()
    local cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        local commands="%s"
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return
    fi
    local verb="${COMP_WORDS[1]}"
    local count=$((COMP_CWORD - 2))
    local args=()
    if [ "$count" -gt 0 ]; then
        args=("${COMP_WORDS[@]:2:$count}")
    fi
    local candidates
    candidates=$(%s %s "$verb" -- "${args[@]}" 2>/dev/null)
    if [ -n "$candidates" ]; then
        COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
    fi
}
complete -F _%s_completions %s
`, r.Name, r.Name, strings.Join(cmds, " "), r.Name, completeVerb, r.Name, r.Name)
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
    local -a args
    if (( CURRENT > 3 )); then
        args=( "${words[@]:2:$((CURRENT - 3))}" )
    fi
    local -a candidates
    candidates=( ${(f)"$(%s %s $verb -- $args 2>/dev/null)"} )
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
`, r.Name, r.Name, completeVerb, r.Name, r.Name, r.Name, r.Name)
	return nil
}

func (r *Router) writeFishCompletion(w io.Writer) error {
	fmt.Fprintf(w, `# fish completion for %s
# Disable filename fallback for this command — completion is fully
# driven by the rules below plus the dynamic __complete dispatcher.
complete -c %s -f

function __%s_dynamic
    set -l tokens (commandline -opc)
    if test (count $tokens) -lt 2
        return
    end
    set -l verb $tokens[2]
    set -l args
    if test (count $tokens) -gt 2
        set args $tokens[3..-1]
    end
    %s %s $verb -- $args 2>/dev/null
end
`, r.Name, r.Name, r.Name, r.Name, completeVerb)
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		desc := strings.ReplaceAll(cmd.Short, "'", "\\'")
		fmt.Fprintf(w, "complete -c %s -n '__fish_use_subcommand' -a %s -d '%s'\n",
			r.Name, cmd.Name, desc)
	}
	fmt.Fprintf(w, "complete -c %s -n 'not __fish_use_subcommand' -f -a '(__%s_dynamic)'\n",
		r.Name, r.Name)
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
