// Package cmdkit is a minimal CLI command-routing framework.
package cmdkit

import "io"

type Command struct {
	Name    string
	Aliases []string
	Group   string
	Short   string
	Long    string

	// Usage defaults to Name.
	Usage string

	Hidden  bool
	ArgsMin int

	// ArgsMax 0 means unlimited.
	ArgsMax int

	Flags []FlagDef
	Run   func(ctx *RunContext) error

	// Live makes the verb hosted rather than run; Run is then the non-interactive fallback.
	Live LiveFunc

	// PassRaw skips flag parsing: everything after the verb goes to RunContext.RawArgs.
	PassRaw bool

	// CompleteFlags are flags a PassRaw command parses itself: completed, never printed by help.
	CompleteFlags []FlagDef

	// CompleteArgs returns every candidate unfiltered; the shell filters by the partial token.
	CompleteArgs func(ctx *CompleteContext) []string
}

type CompleteContext struct {
	// Args are the tokens after the verb, before the cursor; a literal "--" is preserved.
	Args []string

	// Current is the partial token under the cursor, the shell's notion of the current word.
	Current string

	// Describe: the caller asked for "value<TAB>description" lines; the dispatcher strips them otherwise.
	Describe bool

	// PastSeparator: a bare "--" was typed before the cursor.
	PastSeparator bool

	Extra interface{}
}

type FlagDef struct {
	Long        string
	Short       string
	Description string

	// IsBool means the flag takes no value.
	IsBool bool

	// Default empty means unset.
	Default string

	// Subwords names the sub-verbs this flag belongs to; the router refuses it elsewhere before Run.
	Subwords []string
}

type RunContext struct {
	Args           []string
	Flags          map[string]string
	RawArgs        []string
	AfterSeparator []string
	Extra          interface{}

	// In, Out and Err nil mean the router's own, so a verb can run inside a pit or a test.
	In       io.Reader
	Out, Err io.Writer

	Router *Router
}

// Help reports false when no such command is registered.
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

func (c *RunContext) Flag(name string) string {
	return c.Flags[name]
}

func (c *RunContext) HasFlag(name string) bool {
	_, ok := c.Flags[name]
	return ok
}

func (c *RunContext) BoolFlag(name string) bool {
	v, ok := c.Flags[name]
	return ok && v != "false"
}

// LiveView is a hosted verb's model: the host owns the screen, keys and
// dismissal; the view owns its subscription. Capabilities are asserted.
type LiveView interface {
	Close()
}

// ScreenView paints itself into the host's viewport.
type ScreenView interface {
	LiveView
	// Rows returns at most h rows and must not position the cursor.
	Rows(w, h int) []string
}

// KeyView takes keystrokes the host does not own.
type KeyView interface {
	LiveView
	// Key must mutate and return, never repaint: the pager dispatches keys
	// under its render lock. False leaves the key to the host.
	Key(b byte) bool
}

type LiveFunc func(ctx *RunContext) (LiveView, error)
