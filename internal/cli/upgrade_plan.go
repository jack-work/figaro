package cli

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jack-work/figaro/internal/update"
)

type upgradeOptions struct {
	dryRun     bool
	ifChanged  bool
	force      bool
	to         string
	wait       time.Duration
	tmuxSocket string
}

type paneClient struct {
	Pane       string
	Shell      string
	ShellPID   int
	PID        int
	SID        int
	Exe        string
	Release    string
	Argv       []string
	Cwd        string
	Env        []string
	DroppedEnv []string
	Own        bool
	StdinPiped bool
	Attends    string
}

type upgradeAction int

const (
	upgradeRespawn upgradeAction = iota
	upgradeLeave
	upgradeKeep
)

func (a upgradeAction) String() string {
	switch a {
	case upgradeRespawn:
		return "respawn"
	case upgradeKeep:
		return "keep"
	}
	return "leave"
}

type upgradeStep struct {
	Client paneClient
	Action upgradeAction
	Argv   []string
	Reason string
}

const firstReconnectingRelease = "0.54.3"

func reconnectsByItself(release string) bool {
	return update.Compare(release, firstReconnectingRelease) >= 0
}

func planClient(c paneClient) upgradeStep {
	if reconnectsByItself(c.Release) {
		return upgradeStep{Client: c, Action: upgradeKeep, Reason: "figaro " + c.Release + " follows the new angelus by itself"}
	}
	argv, why := replayArgv(c)
	if why == "" {
		why = unreplayable(c, argv)
	}
	if why != "" {
		return upgradeStep{Client: c, Action: upgradeLeave, Reason: why}
	}
	how := "typed again into " + c.Shell + " once it holds the foreground"
	if c.Own {
		how = "the pane's own process: respawn-pane"
	}
	if !slices.Equal(argv, c.Argv) {
		how = "it sent a prompt, so it opens " + shownArgv(argv) + " rather than send it again; " + how
	}
	return upgradeStep{Client: c, Action: upgradeRespawn, Argv: argv, Reason: how}
}

func replayArgv(c paneClient) ([]string, string) {
	if len(c.Argv) == 0 {
		return nil, "its command line is unreadable"
	}
	args := extractNoBindFlag(append([]string(nil), c.Argv[1:]...))
	if !hasDashBoundary(args) {
		return c.Argv, ""
	}
	aria := promptTarget(args)
	if aria == "" {
		aria = c.Attends
	}
	if aria == "" {
		return nil, "it sent a prompt and attends no aria to listen to; open it again with figaro listen <aria>"
	}
	return []string{c.Argv[0], "listen", aria}, ""
}

func promptTarget(args []string) string {
	switch {
	case args[0] == "send" || args[0] == "qua":
		args = args[1:]
	case !strings.HasPrefix(args[0], "-"):
		return ""
	}
	opts, _, err := extractSendFlags(args)
	if err != nil {
		return ""
	}
	if opts.id != "" {
		return opts.id
	}
	return opts.target
}

func unreplayable(c paneClient, argv []string) string {
	if c.StdinPiped {
		return "its input is piped, so the command cannot be typed again"
	}
	for _, a := range argv {
		if a == "--stdin" {
			return "it reads its prompt from stdin, so the command cannot be typed again"
		}
		if holdsControl(a) {
			return "its command line holds a control character, which a terminal would act on before the shell reads the line"
		}
	}
	if c.Own && len(argv) == 1 && strings.Contains(argv[0], "=") {
		return "its command name holds '=', which env would take for a variable"
	}
	if !c.Own && c.ShellPID == 0 {
		return "it is neither the pane's process nor a child of the pane's shell"
	}
	return ""
}

func holdsControl(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

var replayedEnv = map[string]bool{
	"PATH":                    true,
	"FIGARO_ARIA":             true,
	"FIGARO_NO_BIND":          true,
	"FIGARO_RUNTIME_DIR":      true,
	"FIGARO_STATE_DIR":        true,
	"FIGARO_CONFIG_DIR":       true,
	"FIGARO_CACHE_DIR":        true,
	"FIGARO_DEV_ROOT":         true,
	"FIGARO_HUSH_APP":         true,
	"FIGARO_HUSH_DIR":         true,
	"FIGARO_LOG_LEVEL":        true,
	"FIGARO_AMBIGUOUS_WIDE":   true,
	"FIGARO_NO_SCROLL_REGION": true,
}

func replayableEnv(environ []string) (kept, dropped []string) {
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || (name != "PATH" && !strings.HasPrefix(name, "FIGARO_")) {
			continue
		}
		if replayedEnv[name] {
			kept = append(kept, kv)
			continue
		}
		dropped = append(dropped, name)
	}
	return kept, dropped
}

func shownArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if holdsControl(a) {
			q := strconv.Quote(a)
			parts[i] = q[1 : len(q)-1]
			continue
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

func commandLineFor(argv []string, shell string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quoteWord(a, shell)
	}
	return strings.Join(parts, " ")
}

func quoteWord(s, shell string) string {
	if s != "" && strings.IndexFunc(s, needsQuote) < 0 {
		return s
	}
	if strings.Contains(shell, "fish") {
		r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
		return "'" + r.Replace(s) + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuote(r rune) bool {
	if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
		return false
	}
	return !strings.ContainsRune("_./:@%+=,-", r)
}
