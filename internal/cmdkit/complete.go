package cmdkit

import (
	"fmt"
	"io"
	"strings"
)

// CompletionShell identifies a shell for completion script generation.
type CompletionShell string

const (
	ShellBash CompletionShell = "bash"
	ShellZsh  CompletionShell = "zsh"
	ShellFish CompletionShell = "fish"
)

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
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local commands="%s"
    COMPREPLY=($(compgen -W "$commands" -- "$cur"))
}
complete -F _%s_completions %s
`, r.Name, r.Name, strings.Join(cmds, " "), r.Name, r.Name)
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

_%s() {
    _arguments '1: :__%s_commands' '*::arg:->args'
}

_%s "$@"
`, r.Name, r.Name, r.Name)
	return nil
}

func (r *Router) writeFishCompletion(w io.Writer) error {
	fmt.Fprintf(w, "# fish completion for %s\n", r.Name)
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		desc := strings.ReplaceAll(cmd.Short, "'", "\\'")
		fmt.Fprintf(w, "complete -c %s -n '__fish_use_subcommand' -a %s -d '%s'\n",
			r.Name, cmd.Name, desc)
	}
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
