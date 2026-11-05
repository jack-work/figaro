package cli

import (
	"strings"
	"time"
	"unicode"
)

type upgradeOptions struct {
	dryRun      bool
	afterSwitch bool
	force       bool
	to          string
	ref         string
	wait        time.Duration
	tmuxSocket  string
}

type paneClient struct {
	Pane       string
	Shell      string
	ShellPID   int
	PID        int
	Exe        string
	Argv       []string
	Cwd        string
	Env        []string
	Own        bool
	StdinPiped bool
}

type upgradeAction int

const (
	upgradeRespawn upgradeAction = iota
	upgradeLeave
)

func (a upgradeAction) String() string {
	if a == upgradeRespawn {
		return "respawn"
	}
	return "leave"
}

type upgradeStep struct {
	Client paneClient
	Action upgradeAction
	Reason string
}

func planClient(c paneClient) upgradeStep {
	if why := unreplayable(c); why != "" {
		return upgradeStep{Client: c, Action: upgradeLeave, Reason: why}
	}
	if c.Own {
		return upgradeStep{Client: c, Action: upgradeRespawn, Reason: "the pane's own process: respawn-pane"}
	}
	return upgradeStep{Client: c, Action: upgradeRespawn, Reason: "typed again into " + c.Shell + " once it holds the foreground"}
}

func unreplayable(c paneClient) string {
	if c.StdinPiped {
		return "its input is piped, so the command cannot be typed again"
	}
	for _, a := range c.Argv {
		if a == "--stdin" {
			return "it reads its prompt from stdin, so the command cannot be typed again"
		}
	}
	if len(c.Argv) == 0 {
		return "its command line is unreadable"
	}
	if !c.Own && c.ShellPID == 0 {
		return "it is neither the pane's process nor a child of the pane's shell"
	}
	return ""
}

func shellQuote(argv []string, shell string) string {
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
