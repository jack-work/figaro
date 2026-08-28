// Package cmdkit is a minimal CLI command-routing framework.
package cmdkit

import "io"

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

	// Live, when set, makes this a verb that is HOSTED rather than run: it
	// returns a model that renders rows and takes keys. See LiveView.
	Live LiveFunc

	// PassRaw means the router should not parse flags or args -
	// everything after the command name goes into RunContext.RawArgs.
	// Used for commands like prompt that use `-- <text>` conventions.
	PassRaw bool

	// CompleteArgs is an optional callback that returns dynamic
	// completion candidates for this command's positional arguments.
	// The shell filters by the current partial token; return all
	// candidates without prefix-filtering. Return nil to fall back
	// to no completion. Invoked by the hidden __complete dispatcher.
	CompleteArgs func(ctx *CompleteContext) []string
}

// CompleteContext carries state into a CompleteArgs callback.
type CompleteContext struct {
	// Args are the tokens after the command verb, before the cursor.
	// Note: a literal "--" token typed by the user (the conventional
	// flags/prompt separator) IS preserved here so callbacks can
	// distinguish "completing a flag/value" from "completing the
	// prompt body past --". See PastSeparator for the digested form.
	Args []string

	// Current is the partial token under the cursor (may be empty if
	// the cursor sits on a fresh word boundary). Callbacks that want
	// to switch candidate pools based on a sigil prefix (e.g. emit
	// "@key" candidates only when Current starts with "@") use this.
	// The shell-side scripts pass the shell's notion of the current
	// word: ${COMP_WORDS[COMP_CWORD]} in bash, $words[CURRENT] in
	// zsh, (commandline -ct) in fish.
	Current string

	// PastSeparator is true iff the user has already typed a bare "--"
	// token before the cursor (i.e. the cursor lives in the prompt
	// body of `figaro <verb> [flags] -- <body...>`). Useful for
	// switching the candidate pool from flags/ids to prompt-context
	// (form refs, CWD entries, etc.).
	PastSeparator bool

	// Extra mirrors Router.Extra (e.g. *config.Loaded).
	Extra interface{}
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

	// Subwords, when non-empty, names the sub-verbs this flag belongs to.
	// The router refuses the flag BEFORE Run when the command's first
	// positional is not one of them, naming where it does belong.
	Subwords []string
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

	// In, Out and Err are this command's streams. They exist so a verb can be
	// run somewhere that is not a process -- inside the pager's pit, in a
	// test -- without the package reaching for os.Stdout behind everyone's
	// back. Nil means the router's own, which means the process's.
	In       io.Reader
	Out, Err io.Writer

	// Router is the table this command was dispatched from, so a subword
	// dispatcher can delegate to the router's own knowledge: `fig form help
	// <topic>` printing the same page `fig help <topic>` does, rather than a
	// second copy of it that drifts.
	Router *Router
}

// Help prints the help page for a command by name, the way `help <name>`
// would. Reports false when no such command is registered, leaving the
// caller to say so in its own words.
func (c *RunContext) Help(name string) bool {
	if c.Router == nil {
		return false
	}
	cmd, ok := c.Router.Command(name)
	if !ok {
		return false
	}
	c.Router.PrintCommandHelp(cmd)
	return true
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

// ---------------------------------------------------------------------------
// LIVE VIEWS: a verb that is hosted rather than run.
// ---------------------------------------------------------------------------

// LiveView is what a verb becomes when it does not finish: `form listen`
// watches a form until you quit it, `doctor provider --follow` would do the
// same. Such a verb cannot be "run and captured" -- it has no output, it has a
// SCREEN, and it reads keys.
//
// The naive way to host one inside another program is to hand it a pipe and
// let it keep reading os.Stdin. That is worse than it sounds: two readers on
// one fd, each swallowing half the user's keystrokes, and a verb that paints
// with absolute cursor positioning into a region it does not own.
//
// So a live verb is a MODEL instead. It renders rows for a viewport it is
// given and it takes keys as method calls. The host owns the screen, decides
// where the rows go, and decides which keys to forward -- which is what lets
// the same `form listen` be a full-screen command at a shell and a pit in
// the pager, with one implementation and no pipe between them.
type LiveView interface {
	// Rows renders the view for a viewport of w columns and h rows. It must
	// return at most h rows and must not position the cursor: the host places
	// them.
	Rows(w, h int) []string

	// Key offers one keystroke. Reporting false leaves it to the host, which
	// is how Esc closes a pit without the view having to know what a pit
	// is.
	//
	// A KEY HANDLER MUTATES AND RETURNS: it must not repaint. Every host
	// repaints after the key it dispatched, and the pager dispatches keys with
	// its render lock held -- so a view that repaints from in here takes a
	// mutex its caller is already holding, and Go's mutexes do not recurse.
	// Measured: the pager froze, dead, with the view still on screen. What a
	// view may repaint for is what arrives on its own -- a delta, a resync, a
	// failure -- because that is the only thing no host is watching for.
	Key(b byte) bool

	// Hint is what the view can do, for the HOST to place -- on a status line,
	// on a pit's rule, wherever the host keeps affordances. It is not a row,
	// because a view that draws its own footer inside a host that also draws
	// one shows the same sentence twice.
	Hint() string

	// Close releases whatever the view is holding (a subscription, a socket).
	Close()
}

// Live, when set, makes this a live verb: it is HOSTED, not run. A command
// with both Live and Run set uses Live; Run is then the non-interactive
// fallback a script gets.
type LiveFunc func(ctx *RunContext) (LiveView, error)
