package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// ProcessTool operates on backgrounded exec sessions created by the
// bash tool. It shares the bash tool's SessionRegistry and is scoped
// the same way, so a session is only reachable from the scope that
// spawned it.
type ProcessTool struct {
	Sessions *SessionRegistry
	ScopeFn  func() string
}

// NewProcessTool builds a ProcessTool over the given registry. scopeFn
// must match the bash tool's so the two agree on session visibility;
// nil => defaultScope.
func NewProcessTool(sessions *SessionRegistry, scopeFn func() string) *ProcessTool {
	if sessions == nil {
		sessions = NewSessionRegistry(DefaultSessionTTL)
	}
	return &ProcessTool{Sessions: sessions, ScopeFn: scopeFn}
}

func (p *ProcessTool) Name() string { return "process" }

func (p *ProcessTool) Description() string {
	return "Manage backgrounded bash sessions. Actions: list (all sessions), " +
		"poll (status + output since last poll), log (full output), write (send to stdin), " +
		"kill (SIGKILL the process), remove (drop a finished session)."
}

func (p *ProcessTool) Parameters() interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"list", "poll", "log", "write", "kill", "remove"},
				"description": "The verb to run against the session registry",
			},
			"session": map[string]interface{}{
				"type":        "string",
				"description": "Session id (required for all actions except list)",
			},
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Data to send to stdin (write action). A trailing newline is added if absent.",
			},
		},
		"required": []string{"action"},
	}
}

func (p *ProcessTool) scope() string {
	if p.ScopeFn != nil {
		if s := p.ScopeFn(); s != "" {
			return s
		}
	}
	return defaultScope
}

func (p *ProcessTool) Execute(ctx context.Context, args map[string]any, _ OnOutput) ([]message.Content, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	scope := p.scope()

	if action == "list" {
		return text(p.list(scope)), nil
	}

	id, _ := args["session"].(string)
	if id == "" {
		return nil, fmt.Errorf("session is required for action %q", action)
	}

	switch action {
	case "poll":
		sess, ok := p.Sessions.Get(scope, id)
		if !ok {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		return text(formatStatus(sess.Info()) + "\n\n" + nonEmpty(sess.Poll())), nil
	case "log":
		sess, ok := p.Sessions.Get(scope, id)
		if !ok {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		return text(formatStatus(sess.Info()) + "\n\n" + nonEmpty(sess.Log())), nil
	case "write":
		sess, ok := p.Sessions.Get(scope, id)
		if !ok {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		data, _ := args["input"].(string)
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		if err := sess.WriteStdin([]byte(data)); err != nil {
			return nil, err
		}
		return text(fmt.Sprintf("Wrote %d bytes to session %s stdin.", len(data), id)), nil
	case "kill":
		sess, ok := p.Sessions.Get(scope, id)
		if !ok {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		if err := sess.Kill(); err != nil {
			return nil, err
		}
		<-sess.Done()
		return text(formatStatus(sess.Info())), nil
	case "remove":
		sess, ok := p.Sessions.Remove(scope, id)
		if !ok {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		sess.Kill()
		return text(fmt.Sprintf("Removed session %s.", id)), nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func (p *ProcessTool) list(scope string) string {
	sessions := p.Sessions.List(scope)
	if len(sessions) == 0 {
		return "No background sessions."
	}
	infos := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		infos = append(infos, s.Info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].StartedAt.Before(infos[j].StartedAt) })

	var sb strings.Builder
	for _, info := range infos {
		sb.WriteString(formatStatus(info))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatStatus renders a one-line session summary.
func formatStatus(info SessionInfo) string {
	s := fmt.Sprintf("%s [%s] pid=%d", info.ID, info.State, info.Pid)
	if info.State != SessionRunning {
		s += fmt.Sprintf(" exit=%d", info.ExitCode)
	} else {
		s += fmt.Sprintf(" up=%s", time.Since(info.StartedAt).Round(time.Second))
	}
	cmd := info.Command
	if len(cmd) > 60 {
		cmd = cmd[:57] + "..."
	}
	return s + "  " + cmd
}

func nonEmpty(s string) string {
	if s == "" {
		return "(no output)"
	}
	return s
}

func text(s string) []message.Content {
	return []message.Content{message.TextContent(s)}
}
