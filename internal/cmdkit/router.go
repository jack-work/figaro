package cmdkit

import (
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

	// Stderr is the output for help and errors. Defaults to os.Stderr.
	Stderr io.Writer
}

// NewRouter creates a router with the given binary name.
func NewRouter(name string) *Router {
	r := &Router{
		Name:   name,
		index:  make(map[string]*Command),
		Stderr: os.Stderr,
	}
	r.Register(&Command{
		Name:    completeVerb,
		Hidden:  true,
		PassRaw: true,
		Run:     r.runComplete,
	})
	return r
}

// HasCommand reports whether name matches a registered command or alias.
func (r *Router) HasCommand(name string) bool {
	_, ok := r.index[name]
	return ok
}

// Register adds a command to the router.
func (r *Router) Register(cmd *Command) {
	r.commands = append(r.commands, cmd)
	r.index[cmd.Name] = cmd
	for _, a := range cmd.Aliases {
		r.index[a] = cmd
	}
}

// Run dispatches args (without argv[0]). Returns the exit code.
func (r *Router) Run(args []string) int {
	if len(args) == 0 {
		r.printUsage()
		return 2
	}

	// Global flags handled before dispatch.
	first := args[0]
	if first == "--help" || first == "-h" {
		r.printUsage()
		return 0
	}
	if r.Version != "" && (first == "--version" || first == "-V") {
		fmt.Fprintf(r.Stderr, "%s %s\n", r.Name, r.Version)
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
			fmt.Fprintf(r.Stderr, "error: unknown command %q\n", first)
			fmt.Fprintf(r.Stderr, "  did you mean: %s %s\n\n", r.Name, suggestion)
		} else {
			fmt.Fprintf(r.Stderr, "error: unknown command %q\n\n", first)
		}
		r.printUsage()
		return 2
	}

	tail := args[1:]

	// Per-command --help.
	for _, a := range tail {
		if a == "--help" || a == "-h" {
			r.printCommandHelp(cmd)
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
		fmt.Fprintf(r.Stderr, "error: %s\n", err)
		return 1
	}
	return 0
}

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
			name := arg[2:]
			fd := findFlag(cmd.Flags, name, "")
			if fd == nil {
				return nil, fmt.Errorf("unknown flag: --%s", name)
			}
			if fd.IsBool {
				ctx.Flags[fd.Long] = "true"
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

		// Positional arg.
		ctx.Args = append(ctx.Args, arg)
		i++
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

func (r *Router) printUsage() {
	w := r.Stderr

	fmt.Fprintf(w, "Usage: %s <command> [flags] [args]\n\n", r.Name)

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

func (r *Router) printCommandHelp(cmd *Command) {
	w := r.Stderr

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
