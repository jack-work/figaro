package cmdkit

import (
	"fmt"
	"io"
	"strings"
)

// The hidden subcommand the generated completion scripts shell out to.
const completeVerb = "__complete"

// The verb-position token the shell scripts substitute for the bare-prompt form (`figaro -- <body>`).
const barePromptSentinel = "__bare_prompt"

const currentFlag = "--current"

// describeFlag asks for "value<TAB>description" lines; bash cannot take them.
const describeFlag = "--describe"

// Candidate is the protocol's "value<TAB>description" shape; the dispatcher
// strips the description for a caller that did not ask.
func Candidate(value, description string) string {
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return value
	}
	if r := []rune(description); len(r) > maxDescription {
		description = string(r[:maxDescription-1]) + "…"
	}
	return value + "\t" + description
}

const maxDescription = 120

func SplitCandidate(c string) (value, description string) {
	if i := strings.IndexByte(c, '\t'); i >= 0 {
		return c[:i], c[i+1:]
	}
	return c, ""
}

type CompletionShell string

const (
	ShellBash CompletionShell = "bash"
	ShellZsh  CompletionShell = "zsh"
	ShellFish CompletionShell = "fish"
	ShellPwsh CompletionShell = "powershell"
)

// SetBarePromptComplete registers the completer for `figaro -- <body>`; it always runs past the separator.
func (r *Router) SetBarePromptComplete(fn func(*CompleteContext) []string) {
	r.barePromptComplete = fn
}

// Args: [--] <verb> [--describe] [--current <cur>] [--] <tokens...>.
// Each "--" is stripped exactly once; any that survives was typed by the user.
func (r *Router) runComplete(ctx *RunContext) error {
	raw := ctx.RawArgs
	if len(raw) > 0 && raw[0] == "--" {
		raw = raw[1:]
	}
	if len(raw) == 0 {
		return nil
	}
	verb := raw[0]
	tail := raw[1:]
	var current string
	describe := false
	for len(tail) > 0 {
		switch {
		case tail[0] == describeFlag:
			describe = true
			tail = tail[1:]
			continue
		case tail[0] == currentFlag && len(tail) >= 2:
			current = tail[1]
			tail = tail[2:]
			continue
		}
		break
	}
	if len(tail) > 0 && tail[0] == "--" {
		tail = tail[1:]
	}
	pastSep := false
	for _, tok := range tail {
		if tok == "--" {
			pastSep = true
			break
		}
	}

	var fn func(*CompleteContext) []string
	var cmd *Command
	if verb == barePromptSentinel {
		fn = r.barePromptComplete
		pastSep = true
	} else {
		c, ok := r.index[verb]
		if !ok {
			return nil
		}
		cmd, fn = c, c.CompleteArgs
	}

	var cands []string
	flag, value, inline := strings.Cut(current, "=")
	inline = inline && cmd != nil && !pastSep && strings.HasPrefix(flag, "-")
	switch {
	case inline && fn != nil:
		// `--id=<TAB>`: complete the value as if the flag were its own word, then give the flag back.
		for _, c := range fn(&CompleteContext{
			Args:     append(append([]string(nil), tail...), flag),
			Current:  value,
			Describe: describe,
			Extra:    r.Extra,
		}) {
			v, d := SplitCandidate(c)
			cands = append(cands, Candidate(flag+"="+v, d))
		}
	case cmd != nil && !pastSep && strings.HasPrefix(current, "-"):
		cands = flagCandidates(cmd, current)
	case fn != nil:
		cands = fn(&CompleteContext{
			Args:          tail,
			Current:       current,
			PastSeparator: pastSep,
			Describe:      describe,
			Extra:         r.Extra,
		})
	}
	// The router's writer, never os.Stdout: the pager runs this in-process.
	out := ctx.Out
	if out == nil {
		out = r.outw()
	}
	seen := make(map[string]bool, len(cands))
	for _, c := range cands {
		v, d := SplitCandidate(c)
		if v == "" || strings.ContainsAny(v, "\n\r") || seen[v] {
			continue
		}
		seen[v] = true
		if describe {
			fmt.Fprintln(out, Candidate(v, d))
		} else {
			fmt.Fprintln(out, v)
		}
	}
	return nil
}

// "-" alone offers the short forms too.
func flagCandidates(cmd *Command, current string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range append(append([]FlagDef(nil), cmd.Flags...), cmd.CompleteFlags...) {
		if seen[f.Long] {
			continue
		}
		seen[f.Long] = true
		if f.Long != "" {
			out = append(out, Candidate("--"+f.Long, f.Description))
		}
		if f.Short != "" && current == "-" {
			out = append(out, Candidate("-"+f.Short, f.Description))
		}
	}
	return out
}

func (r *Router) WriteCompletion(w io.Writer, shell CompletionShell) error {
	switch shell {
	case ShellBash:
		return r.writeBashCompletion(w)
	case ShellZsh:
		return r.writeZshCompletion(w)
	case ShellFish:
		return r.writeFishCompletion(w)
	case ShellPwsh:
		return r.writePwshCompletion(w)
	default:
		return fmt.Errorf("unsupported shell: %q (use bash, zsh, or fish)", shell)
	}
}

func (r *Router) writeBashCompletion(w io.Writer) error {
	cmds := r.visibleCommandNames()
	// Readline splits words on COMP_WORDBREAKS (@ : =), so the line is split
	// here on whitespace alone and candidates are trimmed of the prefix
	// readline leaves standing. Candidates are read one per line, never
	// through compgen -W, which word-splits and glob-expands them.
	names := strings.Join(append([]string{r.Name}, r.AlsoCalled...), " ")
	fmt.Fprintf(w, `# bash completion for %s
_%s_completions() {
    COMPREPLY=()
    local line="${COMP_LINE:0:COMP_POINT}"
    local -a toks
    read -a toks <<< "$line"
    local cur=""
    if [[ -n "$line" && "$line" != *[[:space:]] ]]; then
        cur="${toks[${#toks[@]}-1]}"
        unset 'toks[${#toks[@]}-1]'
    fi
    local cword=${#toks[@]}
    local verb="${toks[1]}"
    local sentinel="%s"
    local commands="%s"
    local bare=0 w
    for w in "${toks[@]:1}"; do
        if [ "$w" = "--" ]; then bare=1; break; fi
    done
    if [ "$bare" -eq 1 ]; then
        case " $commands " in *" $verb "*) bare=0 ;; esac
    fi
    local -a cands=()
    if [ "$bare" -eq 0 ] && [ "$cword" -eq 1 ]; then
        cands=($commands)
    else
        if [ "$bare" -eq 1 ]; then
            verb="$sentinel"
        fi
        local -a args=("${toks[@]:2}")
        mapfile -t cands < <(%s %s "$verb" --current "$cur" -- "${args[@]}" 2>/dev/null)
    fi
    # What readline will replace is the tail of cur after its last
    # wordbreak; everything before that stays on the line. Except @ and $:
    # bash makes them readline's "special prefixes", word breaks that stay
    # INSIDE the text replaced, so the prefix kept ends just before them.
    # (Trimming through the @ was the bug this rewrite set out to fix,
    # reproduced a second time by a harness that did not know this.)
    local keep="" c
    local wb="${COMP_WORDBREAKS//[[:space:]]/}"
    local i ch
    for ((i = ${#cur} - 1; i >= 0; i--)); do
        ch="${cur:i:1}"
        if [[ "$wb" == *"$ch"* ]]; then
            case "$ch" in
                @|\$) keep="${cur:0:i}" ;;
                *) keep="${cur:0:i+1}" ;;
            esac
            break
        fi
    done
    local nospace=0
    for c in "${cands[@]}"; do
        [[ -z "$c" || "$c" != "$cur"* ]] && continue
        case "$c" in */|*,|*:|*=) nospace=1 ;; esac
        c="${c#"$keep"}"
        # %%q would escape a leading ~ too, and \~ is not the home directory.
        if [[ "$c" == "~/"* ]]; then
            COMPREPLY+=("~/$(printf '%%q' "${c:2}")")
        else
            COMPREPLY+=("$(printf '%%q' "$c")")
        fi
    done
    if [ "$nospace" -eq 1 ]; then
        compopt -o nospace 2>/dev/null
    fi
}
complete -F _%s_completions %s
`, r.Name, r.Name, barePromptSentinel, strings.Join(cmds, " "), r.Name, completeVerb, r.Name, names)
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
	// Every rule carries -f: fish adds filename completion to any rule that
	// does not. Files come back on only for the prompt body past "--".
	fmt.Fprintf(w, `# fish completion for %s
function __%s_dynamic
    set -l tokens (commandline -opc)
    if test (count $tokens) -lt 2
        return
    end
    set -l verb $tokens[2]
    set -l sentinel %s
    if __%s_is_bare_prompt
        set verb $sentinel
    end
    set -l cur (commandline -ct)
    set -l args
    if test (count $tokens) -gt 2
        set args $tokens[3..-1]
    end
    %s %s $verb --describe --current "$cur" -- $args 2>/dev/null
end

# Past a user-typed "--" the words are prompt text, and a path is as good a
# word as any: the one place fish's own file completion is wanted.
function __%s_past_boundary
    contains -- "--" (commandline -opc)[2..-1]
end

# Bare-prompt detector: a "--" boundary ANYWHERE after the program name with a
# non-command in the verb slot. Mirrors cli.isBareForm (hasDashBoundary &&
# !isCommand(args[0])). It used to compare only the SECOND token against the
# boundary, which was right while the bare form took no flags; once
# 'figaro --id A -- <prompt>' became legal the boundary moved past word 2 and
# the detector silently stopped firing.
function __%s_is_bare_prompt
    set -l tokens (commandline -opc)
    if test (count $tokens) -lt 2
        return 1
    end
    if not contains -- "--" $tokens[2..-1]
        return 1
    end
    if contains -- $tokens[2] %s
        return 1
    end
    return 0
end
`, r.Name, r.Name, barePromptSentinel, r.Name, r.Name, completeVerb, r.Name, r.Name, strings.Join(r.visibleCommandNames(), " "))
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		desc := strings.ReplaceAll(cmd.Short, "'", "\\'")
		for _, name := range append([]string{cmd.Name}, cmd.Aliases...) {
			fmt.Fprintf(w, "complete -c %s -f -n '__fish_use_subcommand; and not __%s_is_bare_prompt' -a %s -d '%s'\n",
				r.Name, r.Name, name, desc)
		}
	}
	fmt.Fprintf(w, "complete -c %s -f -n 'not __fish_use_subcommand' -a '(__%s_dynamic)'\n",
		r.Name, r.Name)
	fmt.Fprintf(w, "complete -c %s -f -n '__fish_use_subcommand; and __%s_is_bare_prompt' -a '(__%s_dynamic)'\n",
		r.Name, r.Name, r.Name)
	fmt.Fprintf(w, "complete -c %s -n '__%s_past_boundary' -F\n", r.Name, r.Name)
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

func (r *Router) writePwshCompletion(w io.Writer) error {
	cmds := r.visibleCommandNames()
	name := strings.TrimSuffix(r.Name, ".exe")
	// The executable path is resolved at registration: calling `figaro` inside its completer re-enters it.
	fmt.Fprintf(w, "# PowerShell completion for %s\n", name)
	fmt.Fprintf(w, "$global:_FigaroExe = (Get-Command %s -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1).Source\n", name)
	fmt.Fprintf(w, "if (-not $global:_FigaroExe) { $global:_FigaroExe = '%s.exe' }\n", name)
	fmt.Fprintf(w, "Register-ArgumentCompleter -Native -CommandName %s -ScriptBlock {\n",
		strings.Join(append([]string{name}, r.AlsoCalled...), ", "))
	// $wordToComplete splits on @ and ., so the current word is taken from the raw line.
	fmt.Fprint(w, `    param($wordToComplete, $ast, $cursorPosition)
    $line = $ast.ToString()
    # Detect trailing space: cursor past the AST text means the user typed a space after the last token
    $trailingSpace = $cursorPosition -gt $line.Length
    if ($cursorPosition -lt $line.Length) { $line = $line.Substring(0, $cursorPosition) }
    if ($line -match '(\S+)$') { $curWord = $Matches[1] } else { $curWord = '' }
    $tokens = @($line -split '\s+' | Where-Object { $_ })
    if ($tokens.Count -le 1) { return }
    $verb = $tokens[1]
    if ($tokens.Count -eq 2 -and -not $trailingSpace) {
`)
	fmt.Fprintf(w, "        @(%s) | Where-Object { $_ -like \"$curWord*\" } | ForEach-Object {\n", quotePwshArray(cmds))
	fmt.Fprint(w, `            [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
        }
        return
    }
    $prior = @()
    if ($trailingSpace -and $tokens.Count -gt 2) { $prior = @($tokens[2..($tokens.Count - 1)]) } elseif ($tokens.Count -gt 3) { $prior = @($tokens[2..($tokens.Count - 2)]) }
`)
	fmt.Fprintf(w, "    $sentinel = '%s'\n", barePromptSentinel)
	fmt.Fprint(w, "    if ($verb -eq '--') { $verb = $sentinel }\n")
	fmt.Fprintf(w, "    $candidates = & $global:_FigaroExe %s $verb --current $wordToComplete -- @prior 2>$null\n", completeVerb)
	fmt.Fprint(w, "    if ($candidates) {\n")
	fmt.Fprint(w, "        @($candidates) | ForEach-Object { $_ -split \"`n\" } | Where-Object { $_.Trim() -and ($wordToComplete -eq '' -or $_ -like \"$wordToComplete*\") } | ForEach-Object {\n")
	fmt.Fprint(w, `            [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
        }
    }
}
`)
	return nil
}

func quotePwshArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "'" + s + "'"
	}
	return strings.Join(quoted, ", ")
}
