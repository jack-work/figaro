package cmdkit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// Router dispatches CLI invocations to registered commands.
type Router struct {
	commands []*Command
	index    map[string]*Command // name + aliases → command

	// Name is the binary name shown in help (e.g. "figaro").
	Name string

	// Version is printed by --version. Empty disables the flag.
	Version string

	// Extra is passed to every RunContext.Extra.
	Extra interface{}

	// Fallback is called when no subcommand matches and the args
	// are non-empty. If nil, the router prints usage and exits 2.
	Fallback func(args []string, extra interface{}) error

	// Stdout takes REQUESTED output: --help, help <cmd>, --version. The
	// split is about who asked — help you asked for must be pipeable;
	// usage printed because argv was wrong is a diagnostic (Stderr).
	Stdout io.Writer

	// Stderr is the output for errors and for usage printed as part of an
	// error. Defaults to os.Stderr.
	Stderr io.Writer

	// Synopsis is extra usage lines printed under the Usage header —
	// for forms the router itself does not dispatch (e.g. figaro's bare
	// `figaro [flags] -- <prompt>`).
	Synopsis []string

	// barePromptComplete is the CompleteArgs callback invoked when
	// the user is in the bare-prompt form (`<prog> -- <body>`, or an
	// alias thereof). See SetBarePromptComplete.
	barePromptComplete func(*CompleteContext) []string
}

// NewRouter creates a router with the given binary name.
func NewRouter(name string) *Router {
	r := &Router{
		Name:   name,
		index:  make(map[string]*Command),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	r.Register(&Command{
		Name:    completeVerb,
		Hidden:  true,
		PassRaw: true,
		Run:     r.runComplete,
	})
	r.registerHelp()
	return r
}

// registerHelp installs `help [<command>]`. It lives here, not in each
// consumer's table, because help is the router's own knowledge — and
// because without it `figaro help` answered `unknown command "help", did
// you mean: figaro hup`: the most-guessed verb, pointed at the daemon.
func (r *Router) registerHelp() {
	r.Register(&Command{
		Name:    "help",
		Group:   "System",
		Short:   "Show help for " + r.Name + " or one of its commands",
		Usage:   "help [<command>]",
		ArgsMax: 1,
		Run: func(ctx *RunContext) error {
			if len(ctx.Args) == 0 {
				r.PrintUsage()
				return nil
			}
			name := ctx.Args[0]
			cmd, ok := r.index[name]
			if !ok {
				// Same courtesy the dispatcher extends, and the same exit
				// code: this is misuse, so it must not print help to stdout
				// and claim success.
				fmt.Fprintf(r.errw(), "error: unknown command %q\n", name)
				if s := r.suggest(name); s != "" {
					fmt.Fprintf(r.errw(), "  did you mean: %s help %s\n\n", r.Name, s)
				}
				r.printUsageTo(r.errw())
				return errUsage
			}
			r.PrintCommandHelp(cmd)
			return nil
		},
		CompleteArgs: func(ctx *CompleteContext) []string { return r.CommandNames() },
	})
}

// CommandNames lists the visible command names, for completion.
func (r *Router) CommandNames() []string {
	names := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		if !cmd.Hidden {
			names = append(names, cmd.Name)
		}
	}
	return names
}

// HasCommand reports whether name matches a registered command or alias.
func (r *Router) HasCommand(name string) bool {
	_, ok := r.index[name]
	return ok
}

// Command looks up a registered command by name or alias.
func (r *Router) Command(name string) (*Command, bool) {
	cmd, ok := r.index[name]
	return cmd, ok
}

// ReservedShorts are single-letter flags the ROUTER answers before any
// command sees them, so a command declaring one can never receive it.
// `ls` declared -h for --home and advertised it in the help text that -h
// itself printed; `ls -h` gave help while `ls -ha` gave home. Making the
// collision impossible to write beats adjudicating it per command.
var ReservedShorts = map[string]string{
	"h": "help",
	"V": "version",
}

// Register adds a command to the router. It panics if the command claims a
// reserved short: tables are built from literals at startup, so this fires
// on the developer at first construction, never silently on the user.
func (r *Router) Register(cmd *Command) {
	for _, f := range cmd.Flags {
		if owner, taken := ReservedShorts[f.Short]; taken {
			panic(fmt.Sprintf(
				"cmdkit: command %q declares -%s for --%s, but -%s is reserved for %s "+
					"and is answered before the command runs; give --%s another short or none",
				cmd.Name, f.Short, f.Long, f.Short, owner, f.Long))
		}
	}
	r.commands = append(r.commands, cmd)
	r.index[cmd.Name] = cmd
	for _, a := range cmd.Aliases {
		r.index[a] = cmd
	}
}

// ValidateReservedShorts reports every registered flag that claims a
// reserved short. Register panics on the same condition; this is the
// non-fatal form, for a test that wants all offenders named at once.
func (r *Router) ValidateReservedShorts() error {
	var bad []string
	for _, cmd := range r.commands {
		for _, f := range cmd.Flags {
			if owner, taken := ReservedShorts[f.Short]; taken {
				bad = append(bad, fmt.Sprintf("%s: -%s (--%s) collides with %s", cmd.Name, f.Short, f.Long, owner))
			}
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("reserved shorts claimed by commands: %s", strings.Join(bad, "; "))
	}
	return nil
}

// Run dispatches args (without argv[0]). Returns the exit code.
func (r *Router) Run(args []string) int {
	if len(args) == 0 {
		// Nobody asked for this: it is a misuse diagnostic. Stderr, exit 2.
		r.printUsageTo(r.errw())
		return 2
	}

	// Global flags handled before dispatch.
	first := args[0]
	if first == "--help" || first == "-h" {
		r.PrintUsage()
		return 0
	}
	if r.Version != "" && (first == "--version" || first == "-V") {
		fmt.Fprintf(r.outw(), "%s %s\n", r.Name, r.Version)
		return 0
	}

	// Lookup command.
	cmd, ok := r.index[first]
	if !ok {
		// Try fallback.
		if r.Fallback != nil {
			if err := r.Fallback(args, r.Extra); err != nil {
				fmt.Fprintf(r.Stderr, "error: %s\n", err)
				return 1
			}
			return 0
		}
		// Did-you-mean suggestion.
		if suggestion := r.suggest(first); suggestion != "" {
			fmt.Fprintf(r.errw(), "error: unknown command %q\n", first)
			fmt.Fprintf(r.errw(), "  did you mean: %s %s\n\n", r.Name, suggestion)
		} else {
			fmt.Fprintf(r.errw(), "error: unknown command %q\n\n", first)
		}
		r.printUsageTo(r.errw())
		return 2
	}

	tail := args[1:]

	// Per-command --help.
	for _, a := range tail {
		if a == "--help" || a == "-h" {
			r.PrintCommandHelp(cmd)
			return 0
		}
		if a == "--" {
			break
		}
	}

	// Parse flags + args.
	ctx, err := r.parse(cmd, tail)
	if err != nil {
		fmt.Fprintf(r.Stderr, "error: %s %s: %s\n", r.Name, cmd.Name, err)
		return 2
	}
	ctx.Extra = r.Extra

	// Run the command.
	if err := cmd.Run(ctx); err != nil {
		if errors.Is(err, errUsage) {
			// The command already printed its own diagnostic; this is
			// misuse, so it exits 2 like every other rejected argv.
			return 2
		}
		fmt.Fprintf(r.errw(), "error: %s\n", err)
		return 1
	}
	return 0
}

// errUsage lets a command report "argv was rejected, and I have already
// said why" — the router turns it into exit 2 with no second message.
var errUsage = errors.New("usage")

// parse processes flags and positional args for a command.
func (r *Router) parse(cmd *Command, args []string) (*RunContext, error) {
	ctx := &RunContext{
		Flags: make(map[string]string),
	}

	if cmd.PassRaw {
		ctx.RawArgs = args
		return ctx, nil
	}

	// Apply defaults.
	for _, f := range cmd.Flags {
		if f.Default != "" {
			ctx.Flags[f.Long] = f.Default
		}
	}

	// Expand bundled short flags: -avl → -a -v -l
	expanded := expandBundled(args, cmd.Flags)

	i := 0
	for i < len(expanded) {
		arg := expanded[i]

		// End of flags.
		if arg == "--" {
			ctx.Args = append(ctx.Args, expanded[i+1:]...)
			break
		}

		// Long flag.
		if strings.HasPrefix(arg, "--") {
			// `--name=value`: the whole token used to be looked up, so
			// `--limit=5` was "unknown flag" while send.go took `--id=x`.
			name := arg[2:]
			inline, hasInline := "", false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, inline, hasInline = name[:eq], name[eq+1:], true
			}
			fd := findFlag(cmd.Flags, name, "")
			if fd == nil {
				return nil, fmt.Errorf("unknown flag: --%s", name)
			}
			if fd.IsBool {
				// A bool may carry an explicit truth value — `--attend=false`
				// is a form send.go already honours — but nothing else.
				val := "true"
				if hasInline {
					switch inline {
					case "true", "1":
						val = "true"
					case "false", "0":
						val = "false"
					default:
						return nil, fmt.Errorf("flag --%s takes no value (got %q); use --%s or --%s=false", name, inline, name, name)
					}
				}
				ctx.Flags[fd.Long] = val
			} else if hasInline {
				if inline == "" {
					return nil, fmt.Errorf("flag --%s requires a value", name)
				}
				ctx.Flags[fd.Long] = inline
			} else {
				i++
				if i >= len(expanded) {
					return nil, fmt.Errorf("flag --%s requires a value", name)
				}
				ctx.Flags[fd.Long] = expanded[i]
			}
			i++
			continue
		}

		// Short flag.
		if len(arg) == 2 && arg[0] == '-' && arg[1] != '-' {
			ch := string(arg[1])
			fd := findFlag(cmd.Flags, "", ch)
			if fd == nil {
				return nil, fmt.Errorf("unknown flag: -%s", ch)
			}
			if fd.IsBool {
				ctx.Flags[fd.Long] = "true"
			} else {
				i++
				if i >= len(expanded) {
					return nil, fmt.Errorf("flag -%s requires a value", ch)
				}
				ctx.Flags[fd.Long] = expanded[i]
			}
			i++
			continue
		}

		// A '-' token that survived both flag branches and bundle expansion
		// is argv nobody consumed. Treating it as a positional is how
		// `ls -hz` meant "the aria named -hz" and `kill -rx` aimed a
		// destructive verb at a typo. A bare "-" is exempt (stdin).
		if len(arg) > 1 && arg[0] == '-' {
			return nil, unconsumedFlagError(cmd.Flags, arg)
		}

		// Positional arg.
		ctx.Args = append(ctx.Args, arg)
		i++
		continue
	}

	// Validate arg count.
	if cmd.ArgsMin > 0 && len(ctx.Args) < cmd.ArgsMin {
		return nil, fmt.Errorf("requires at least %d argument(s)", cmd.ArgsMin)
	}
	if cmd.ArgsMax > 0 && len(ctx.Args) > cmd.ArgsMax {
		return nil, fmt.Errorf("accepts at most %d argument(s), got %d", cmd.ArgsMax, len(ctx.Args))
	}

	return ctx, nil
}

// unconsumedFlagError explains a dash-token that survived both the flag
// branches and bundle expansion. It names the exact letters at fault so a
// typo'd gang (`-hz`) and a mis-bundled value flag (`-an`, where -n takes a
// value) get different, actionable messages.
func unconsumedFlagError(flags []FlagDef, tok string) error {
	var unknown []string
	var valued []string
	for _, r := range tok[1:] {
		fd := findFlag(flags, "", string(r))
		switch {
		case fd == nil:
			unknown = append(unknown, "-"+string(r))
		case !fd.IsBool:
			valued = append(valued, fmt.Sprintf("-%s/--%s", string(r), fd.Long))
		}
	}
	switch {
	case len(unknown) > 0:
		// Teach the escape hatch in the same breath: a legitimate value that
		// happens to start with '-' (`set mantra -x`) is now rejected here,
		// and `--` is how it gets through.
		return fmt.Errorf("unknown flag %q (unrecognized in the bundle: %s); if it is a value, put it after `--`",
			tok, strings.Join(unknown, ", "))
	case len(valued) > 0:
		return fmt.Errorf("cannot bundle %q: %s take(s) a value — pass it on its own", tok, strings.Join(valued, ", "))
	default:
		// Every letter is a known bool short, so expandBundled should have
		// taken it. Unreachable today; report rather than swallow.
		return fmt.Errorf("unparsed flag %q", tok)
	}
}

// ExpandBundled expands short-flag gangs (-ex -> -e -x) against a flag
// table. Exported so the PassRaw parsers expand from the same table the
// router does: two expanders is how `-fj` came to fail while `-f -j` worked.
func ExpandBundled(args []string, flags []FlagDef) []string { return expandBundled(args, flags) }

func expandBundled(args []string, flags []FlagDef) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			// Check that all chars are known bool short flags.
			allBool := true
			for _, r := range a[1:] {
				fd := findFlag(flags, "", string(r))
				if fd == nil || !fd.IsBool {
					allBool = false
					break
				}
			}
			if allBool {
				for _, r := range a[1:] {
					out = append(out, "-"+string(r))
				}
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func findFlag(flags []FlagDef, long, short string) *FlagDef {
	for i := range flags {
		if long != "" && flags[i].Long == long {
			return &flags[i]
		}
		if short != "" && flags[i].Short == short {
			return &flags[i]
		}
	}
	return nil
}

// Suggest returns the registered command name closest to input by
// Levenshtein distance, or "" when nothing is close enough. Exported so
// callers that dispatch outside Run (the bare `figaro -- <prompt>` form)
// can offer the same did-you-mean hint.
func (r *Router) Suggest(input string) string { return r.suggest(input) }

// suggest finds the closest command name by Levenshtein distance.
func (r *Router) suggest(input string) string {
	best := ""
	bestDist := 4 // threshold
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		names := append([]string{cmd.Name}, cmd.Aliases...)
		for _, name := range names {
			d := levenshtein(input, name)
			if d < bestDist {
				bestDist = d
				best = name
			}
		}
	}
	return best
}

func levenshtein(a, b string) int {
	la := utf8.RuneCountInString(a)
	lb := utf8.RuneCountInString(b)
	ra := []rune(a)
	rb := []rune(b)

	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

// --- Help output ---

// outw/errw resolve the writers, tolerating a zero-value Router (a Router
// built as a struct literal rather than through NewRouter still prints).
func (r *Router) outw() io.Writer {
	if r.Stdout == nil {
		return os.Stdout
	}
	return r.Stdout
}

func (r *Router) errw() io.Writer {
	if r.Stderr == nil {
		return os.Stderr
	}
	return r.Stderr
}

// PrintUsage writes the top-level help to Stdout. Exported so a `help`
// verb (and figaro's bare-prompt dispatcher) can reach it.
func (r *Router) PrintUsage() { r.printUsageTo(r.outw()) }

func (r *Router) printUsageTo(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <command> [flags] [args]\n", r.Name)
	for _, line := range r.Synopsis {
		fmt.Fprintf(w, "       %s\n", line)
	}
	fmt.Fprintln(w)

	// Group commands.
	groups := r.groupedCommands()
	for _, g := range groups {
		fmt.Fprintf(w, "%s:\n", g.name)
		for _, cmd := range g.commands {
			name := cmd.Name
			if cmd.Usage != "" {
				name = cmd.Usage
			}
			fmt.Fprintf(w, "  %-24s %s\n", name, cmd.Short)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Run '%s <command> --help' for details on a command.\n", r.Name)
}

// PrintCommandHelp writes one command's help to Stdout.
func (r *Router) PrintCommandHelp(cmd *Command) { r.printCommandHelpTo(r.outw(), cmd) }

func (r *Router) printCommandHelpTo(w io.Writer, cmd *Command) {
	usage := cmd.Usage
	if usage == "" {
		usage = cmd.Name
	}
	fmt.Fprintf(w, "Usage: %s %s\n\n", r.Name, usage)

	if cmd.Long != "" {
		fmt.Fprintln(w, cmd.Long)
		fmt.Fprintln(w)
	} else if cmd.Short != "" {
		fmt.Fprintln(w, cmd.Short)
		fmt.Fprintln(w)
	}

	if len(cmd.Flags) > 0 {
		fmt.Fprintln(w, "Flags:")
		for _, f := range cmd.Flags {
			short := ""
			if f.Short != "" {
				short = "-" + f.Short + ", "
			}
			fmt.Fprintf(w, "  %s--%s\t%s\n", short, f.Long, f.Description)
		}
		fmt.Fprintln(w)
	}

	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(w, "Aliases: %s\n", strings.Join(cmd.Aliases, ", "))
	}
}

type commandGroup struct {
	name     string
	commands []*Command
}

func (r *Router) groupedCommands() []commandGroup {
	gmap := map[string][]*Command{}
	var order []string
	for _, cmd := range r.commands {
		if cmd.Hidden {
			continue
		}
		g := cmd.Group
		if g == "" {
			g = "Commands"
		}
		if _, ok := gmap[g]; !ok {
			order = append(order, g)
		}
		gmap[g] = append(gmap[g], cmd)
	}
	// Sort commands within each group by name.
	for _, cmds := range gmap {
		sort.Slice(cmds, func(i, j int) bool {
			return cmds[i].Name < cmds[j].Name
		})
	}
	groups := make([]commandGroup, 0, len(order))
	for _, name := range order {
		groups = append(groups, commandGroup{name: name, commands: gmap[name]})
	}
	return groups
}
