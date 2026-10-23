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

type Router struct {
	commands []*Command
	index    map[string]*Command

	Name string

	// Version empty disables --version.
	Version string

	Extra interface{}

	// AlsoCalled are other names the binary is installed under; completion scripts register them too.
	AlsoCalled []string

	// Fallback runs when no subcommand matches; nil prints usage and exits 2.
	Fallback func(args []string, extra interface{}) error

	// Stdout takes requested output (--help, --version); usage printed for bad argv goes to Stderr.
	Stdout io.Writer
	Stderr io.Writer

	// Synopsis is extra usage lines for forms the router does not dispatch.
	Synopsis []string

	// GlobalFlags are consumed by the caller before dispatch; listed here only for help.
	GlobalFlags []FlagDef

	barePromptComplete func(*CompleteContext) []string
}

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

func (r *Router) CommandNames() []string {
	names := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		if !cmd.Hidden {
			names = append(names, cmd.Name)
		}
	}
	return names
}

// CommandCandidates is CommandNames in the completion protocol's "value<TAB>description" shape.
func (r *Router) CommandCandidates() []string {
	out := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		if !cmd.Hidden {
			out = append(out, Candidate(cmd.Name, cmd.Short))
		}
	}
	return out
}

func (r *Router) HasCommand(name string) bool {
	_, ok := r.index[name]
	return ok
}

func (r *Router) Command(name string) (*Command, bool) {
	cmd, ok := r.index[name]
	return cmd, ok
}

// ReservedShorts are answered by the router before any command sees them.
var ReservedShorts = map[string]string{
	"h": "help",
	"V": "version",
}

// Register panics on a reserved short.
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

// ValidateReservedShorts is the non-fatal form of Register's check, naming every offender.
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

// Run dispatches args (without argv[0]) and returns the exit code.
func (r *Router) Run(args []string) int {
	if len(args) == 0 {
		r.printUsageTo(r.errw())
		return 2
	}

	first := args[0]
	if first == "--help" || first == "-h" {
		r.PrintUsage()
		return 0
	}
	if r.Version != "" && (first == "--version" || first == "-V") {
		fmt.Fprintf(r.outw(), "%s %s\n", r.Name, r.Version)
		return 0
	}

	cmd, ok := r.index[first]
	if !ok {
		if r.Fallback != nil {
			if err := r.Fallback(args, r.Extra); err != nil {
				fmt.Fprintf(r.Stderr, "error: %s\n", err)
				return 1
			}
			return 0
		}
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

	for _, a := range tail {
		if a == "--help" || a == "-h" {
			r.PrintCommandHelp(cmd)
			return 0
		}
		if a == "--" {
			break
		}
	}

	ctx, err := r.parse(cmd, first, tail)
	if err != nil {
		fmt.Fprintf(r.Stderr, "error: %s %s: %s\n", r.Name, first, err)
		return 2
	}
	ctx.Extra = r.Extra

	if err := cmd.Run(ctx); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		fmt.Fprintf(r.errw(), "error: %s\n", err)
		return 1
	}
	return 0
}

// errUsage: the command already printed its diagnostic; the router exits 2 silently.
var errUsage = errors.New("usage")

func (r *Router) Parse(name string, args []string) (*RunContext, error) {
	cmd, ok := r.Command(name)
	if !ok {
		return nil, fmt.Errorf("unknown command: %s", name)
	}
	return r.parse(cmd, name, args)
}

// verb is the name as typed (possibly an alias), so messages echo what the user wrote.
func (r *Router) parse(cmd *Command, verb string, args []string) (*RunContext, error) {
	if verb == "" {
		verb = cmd.Name
	}

	ctx := &RunContext{
		Flags:  make(map[string]string),
		Router: r,
	}

	if cmd.PassRaw {
		ctx.RawArgs = args
		return ctx, nil
	}

	for _, f := range cmd.Flags {
		if f.Default != "" {
			ctx.Flags[f.Long] = f.Default
		}
	}

	expanded := expandBundled(args, cmd.Flags)

	i := 0
	for i < len(expanded) {
		arg := expanded[i]

		if arg == "--" {
			ctx.AfterSeparator = append([]string{}, expanded[i+1:]...)
			ctx.Args = append(ctx.Args, ctx.AfterSeparator...)
			break
		}

		if strings.HasPrefix(arg, "--") {
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

		// An unconsumed '-' token is never a positional; a bare "-" (stdin) is exempt.
		if len(arg) > 1 && arg[0] == '-' {
			return nil, unconsumedFlagError(cmd.Flags, arg)
		}

		ctx.Args = append(ctx.Args, arg)
		i++
		continue
	}

	// A flag belonging to a sub-verb the caller did not type is refused, not ignored.
	for _, fd := range cmd.Flags {
		if len(fd.Subwords) == 0 {
			continue
		}
		if _, given := ctx.Flags[fd.Long]; !given {
			continue
		}
		sub := ""
		if len(ctx.Args) > 0 {
			sub = ctx.Args[0]
		}
		if !containsString(fd.Subwords, sub) {
			return nil, subwordFlagError(verb, fd, sub)
		}
	}

	if cmd.ArgsMin > 0 && len(ctx.Args) < cmd.ArgsMin {
		return nil, fmt.Errorf("requires at least %d argument(s)", cmd.ArgsMin)
	}
	if cmd.ArgsMax > 0 && len(ctx.Args) > cmd.ArgsMax {
		return nil, fmt.Errorf("accepts at most %d argument(s), got %d", cmd.ArgsMax, len(ctx.Args))
	}

	return ctx, nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func subwordFlagError(cmd string, fd FlagDef, sub string) error {
	where := make([]string, 0, len(fd.Subwords))
	for _, w := range fd.Subwords {
		where = append(where, "`"+cmd+" "+w+"`")
	}
	got := "on its own"
	if sub != "" {
		got = "`" + cmd + " " + sub + "`"
	}
	return fmt.Errorf("--%s belongs to %s, not %s", fd.Long, strings.Join(where, ", "), got)
}

// A bundle reaches here only when one of its letters is unknown.
func unconsumedFlagError(flags []FlagDef, tok string) error {
	var unknown []string
	for _, r := range tok[1:] {
		if findFlag(flags, "", string(r)) == nil {
			unknown = append(unknown, "-"+string(r))
		}
	}
	if len(unknown) == 0 {
		return fmt.Errorf("unparsed flag %q", tok)
	}
	return fmt.Errorf("unknown flag %q (unrecognized in the bundle: %s); if it is a value, put it after `--`",
		tok, strings.Join(unknown, ", "))
}

// ExpandBundled is the one expander; PassRaw parsers must use it too.
func ExpandBundled(args []string, flags []FlagDef) []string { return expandBundled(args, flags) }

// A value-taking short ends the bundle getopt-style (`-eOsonn5`, `-eO sonn5`);
// a bundle with an unknown letter is left as typed.
func expandBundled(args []string, flags []FlagDef) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			if expanded, ok := splitBundle(a[1:], flags); ok {
				out = append(out, expanded...)
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func splitBundle(letters string, flags []FlagDef) ([]string, bool) {
	var out []string
	for i, r := range letters {
		fd := findFlag(flags, "", string(r))
		if fd == nil {
			return nil, false
		}
		out = append(out, "-"+string(r))
		if fd.IsBool {
			continue
		}
		if rest := letters[i+len(string(r)):]; rest != "" {
			out = append(out, rest)
		}
		return out, true
	}
	return out, true
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

// Suggest returns the closest command name by Levenshtein distance, or "".
func (r *Router) Suggest(input string) string { return r.suggest(input) }

func (r *Router) suggest(input string) string {
	best := ""
	bestDist := 4
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

func (r *Router) PrintUsage() { r.printUsageTo(r.outw()) }

func (r *Router) printUsageTo(w io.Writer) {
	fmt.Fprintf(w, "Usage: %s <command> [flags] [args]\n", r.Name)
	for _, line := range r.Synopsis {
		fmt.Fprintf(w, "       %s\n", line)
	}
	fmt.Fprintln(w)

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

	if len(r.GlobalFlags) > 0 {
		fmt.Fprintln(w, "Global flags (any command):")
		for _, f := range r.GlobalFlags {
			name := "--" + f.Long
			if f.Short != "" {
				name = "-" + f.Short + ", " + name
			}
			fmt.Fprintf(w, "  %-24s %s\n", name, f.Description)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Run '%s <command> --help' for details on a command.\n", r.Name)
}

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
