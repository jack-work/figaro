package figaro

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/jack-work/figaro/api/message"
)

type replayTape struct {
	calls map[string][]toolOutcome
}

func (r *replayTurn) answer(tc message.Content) toolOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	if oc, ok := r.tape.take(tc); ok {
		return oc
	}
	return declaredStub(tc)
}

func recordTape(turn []message.Message) *replayTape {
	tape := &replayTape{calls: map[string][]toolOutcome{}}
	answers := map[string]toolOutcome{}
	for _, m := range turn {
		for _, c := range m.Content {
			switch {
			case c.Type == message.ContentToolResult:
				oc := answers[c.ToolCallID]
				oc.content = append([]message.Content{message.TextContent(c.Text)}, oc.content...)
				oc.isErr = c.IsError
				answers[c.ToolCallID] = oc
			case c.Type == message.ContentImage && c.ToolCallID != "":
				oc := answers[c.ToolCallID]
				oc.content = append(oc.content, message.ImageContent(c.MimeType, c.Data))
				answers[c.ToolCallID] = oc
			case c.Type == message.ContentInterrupt && c.ToolCallID != "":
				text := c.Text
				if text == "" {
					text = "(tool execution was interrupted)"
				}
				answers[c.ToolCallID] = toolOutcome{content: []message.Content{message.TextContent(text)}, isErr: true}
			}
		}
	}
	for _, m := range turn {
		if m.Role != message.RoleOutput {
			continue
		}
		for _, c := range m.Content {
			if c.Type != message.ContentToolInvoke {
				continue
			}
			if oc, ok := answers[c.ToolCallID]; ok && len(oc.content) > 0 {
				k := callKey(c)
				tape.calls[k] = append(tape.calls[k], oc)
			}
		}
	}
	return tape
}

func (t *replayTape) take(tc message.Content) (toolOutcome, bool) {
	k := callKey(tc)
	q := t.calls[k]
	if len(q) == 0 {
		return toolOutcome{}, false
	}
	if len(q) > 1 {
		t.calls[k] = q[1:]
	}
	return q[0], true
}

func callKey(c message.Content) string {
	args := c.Arguments
	if args == nil {
		args = map[string]interface{}{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return c.ToolName
	}
	return c.ToolName + "\x00" + string(b)
}

func declaredStub(tc message.Content) toolOutcome {
	str := func(k string) string {
		s, _ := tc.Arguments[k].(string)
		return s
	}
	text := func(s string) toolOutcome {
		return toolOutcome{content: []message.Content{message.TextContent(s)}}
	}
	switch tc.ToolName {
	case "write", "edit":
		return text(str("path"))
	case "say":
		return text("posted")
	case "bash":
		if outwardCommand(str("command")) {
			return text("(no output)")
		}
		return text("(no output)\n\nCommand exited with code 1")
	}
	return toolOutcome{content: []message.Content{message.TextContent("Error: resource temporarily unavailable")}, isErr: true}
}

var (
	outwardBinaries = map[string]bool{
		"figaro": true, "fig": true, "figla": true, "gh": true, "herald": true,
		"rm": true, "mv": true, "cp": true, "mkdir": true, "touch": true, "tee": true,
		"ln": true, "chmod": true, "chown": true, "truncate": true, "install": true,
		"kill": true, "pkill": true, "systemctl": true, "curl": true, "wget": true,
	}
	gitWrites = map[string]bool{
		"add": true, "am": true, "apply": true, "checkout": true, "cherry-pick": true,
		"clean": true, "commit": true, "merge": true, "mv": true, "pull": true, "push": true,
		"rebase": true, "reset": true, "restore": true, "revert": true, "rm": true,
		"stash": true, "switch": true, "tag": true, "worktree": true,
	}
	fileRedirect = regexp.MustCompile(`(^|[^0-9&<>])>>?\s*([^&\s>]+)`)
)

func outwardCommand(cmd string) bool {
	for _, m := range fileRedirect.FindAllStringSubmatch(cmd, -1) {
		if m[2] != "/dev/null" {
			return true
		}
	}
	segments := strings.FieldsFunc(cmd, func(r rune) bool {
		return strings.ContainsRune(";|&()`\n", r)
	})
	for _, seg := range segments {
		words := commandWords(seg)
		if len(words) == 0 {
			continue
		}
		switch base := path.Base(words[0]); {
		case outwardBinaries[base]:
			return true
		case base == "git":
			if gitWrites[gitSubcommand(words[1:])] {
				return true
			}
		case base == "sed" || base == "perl":
			for _, w := range words[1:] {
				if strings.HasPrefix(w, "-i") {
					return true
				}
			}
		}
	}
	return false
}

var (
	commandWrappers = map[string]bool{"env": true, "sudo": true, "command": true, "exec": true, "nohup": true, "time": true, "xargs": true}
	takesValue      = map[string]bool{"-u": true, "-c": true, "-C": true, "--git-dir": true, "--work-tree": true}
)

func commandWords(seg string) []string {
	ws := strings.Fields(seg)
	for i := 0; i < len(ws); {
		w := ws[i]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"):
			i++
		case commandWrappers[path.Base(w)]:
			i++
			for i < len(ws) && strings.HasPrefix(ws[i], "-") {
				if takesValue[ws[i]] {
					i++
				}
				i++
			}
		default:
			return ws[i:]
		}
	}
	return nil
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
		if takesValue[args[i]] {
			i++
		}
	}
	return ""
}
