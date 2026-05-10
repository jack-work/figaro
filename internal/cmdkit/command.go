// Package cmdkit is a minimal command-routing framework for CLI
// applications. It provides command registration, dispatch, --help
// generation, did-you-mean suggestions, and shell completion —
// without application-specific dependencies.
package cmdkit

// Command defines a single CLI subcommand.
type Command struct {
	// Name is the primary command name (e.g. "list", "kill").
	Name string

	// Aliases are alternative names that also dispatch to this command.
	Aliases []string

	// Group categorizes the command in help output.
	Group string

	// Short is a one-line description shown in the command listing.
	Short string

	// Long is the detailed help shown by `<cmd> --help`.
	Long string

	// Usage is the usage line (e.g. "kill <id>").
	// If empty, defaults to Name.
	Usage string

	// Hidden hides the command from help listings (e.g. internal commands).
	Hidden bool

	// ArgsMin is the minimum number of positional args required.
	ArgsMin int

	// ArgsMax is the maximum number of positional args allowed.
	// 0 means unlimited (the default). Use a positive value to cap.
	ArgsMax int

	// Flags defines the accepted flags for this command.
	Flags []FlagDef

	// Run is the command implementation. It receives the parsed context.
	// Return nil on success, an error on failure.
	Run func(ctx *RunContext) error

	// PassRaw means the router should not parse flags or args —
	// everything after the command name goes into RunContext.RawArgs.
	// Used for commands like prompt that use `-- <text>` conventions.
	PassRaw bool
}

// FlagDef describes a flag accepted by a command.
type FlagDef struct {
	// Long is the --name form (without the --).
	Long string

	// Short is the single-character -x form (without the -). Empty means no short form.
	Short string

	// Description is shown in --help.
	Description string

	// IsBool means the flag takes no value (presence = true).
	IsBool bool

	// Default is the default value (string form). Empty = unset.
	Default string
}

// RunContext carries parsed state into a command's Run function.
type RunContext struct {
	// Args are the positional arguments after flag parsing.
	Args []string

	// Flags holds the parsed flag values, keyed by long name.
	Flags map[string]string

	// RawArgs is the full unparsed arg tail (for PassRaw commands).
	RawArgs []string

	// Extra is caller-provided data (e.g. *config.Loaded, dependencies).
	Extra interface{}
}

// Flag returns the value of a flag by long name. Returns "" if unset.
func (c *RunContext) Flag(name string) string {
	return c.Flags[name]
}

// HasFlag reports whether a flag was explicitly set.
func (c *RunContext) HasFlag(name string) bool {
	_, ok := c.Flags[name]
	return ok
}

// BoolFlag returns true if the named boolean flag was set.
func (c *RunContext) BoolFlag(name string) bool {
	v, ok := c.Flags[name]
	return ok && v != "false"
}
