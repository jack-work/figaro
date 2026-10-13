package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/sandbox"
)

type ProcessTool struct {
	Sessions sandbox.Sessions
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

func (p *ProcessTool) Execute(ctx context.Context, args map[string]any, _ OnOutput) ([]message.Content, error) {
	action, _ := args["action"].(string)
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	if action == "list" {
		return text(p.list()), nil
	}
	id, _ := args["session"].(string)
	if id == "" {
		return nil, fmt.Errorf("session is required for action %q", action)
	}
	if action == "remove" {
		if err := p.Sessions.Remove(id); err != nil {
			return nil, fmt.Errorf("unknown session %q", id)
		}
		return text(fmt.Sprintf("Removed session %s.", id)), nil
	}
	sess, err := p.Sessions.Get(id)
	if err != nil {
		return nil, fmt.Errorf("unknown session %q", id)
	}
	switch action {
	case "poll":
		return text(formatStatus(sess.Info()) + "\n\n" + nonEmpty(string(sess.Poll()))), nil
	case "log":
		return text(formatStatus(sess.Info()) + "\n\n" + nonEmpty(string(sess.Log()))), nil
	case "write":
		data, _ := args["input"].(string)
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		if _, err := sess.Write([]byte(data)); err != nil {
			return nil, err
		}
		return text(fmt.Sprintf("Wrote %d bytes to session %s stdin.", len(data), id)), nil
	case "kill":
		if err := sess.Kill(); err != nil {
			return nil, err
		}
		<-sess.Done()
		return text(formatStatus(sess.Info())), nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func (p *ProcessTool) list() string {
	infos := p.Sessions.List()
	if len(infos) == 0 {
		return "No background sessions."
	}
	lines := make([]string, 0, len(infos))
	for _, info := range infos {
		lines = append(lines, formatStatus(info))
	}
	return strings.Join(lines, "\n")
}

func formatStatus(info sandbox.SessionInfo) string {
	s := fmt.Sprintf("%s [%s]", info.ID, info.State)
	if info.State != sandbox.SessionRunning {
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
