package figaro

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// InterruptedToolNotice is the text a tool_result carries when its tool never
// finished because the turn was interrupted. It is EXPORTED because it is the
// only DURABLE mark an interrupt leaves: repairTurnTail runs when the agent
// knows it was interrupted, and a client that merely dies never makes it run.
const InterruptedToolNotice = "interrupted: tool execution did not complete"

type turnTool struct {
	ToolCallID string
	ToolName   string
	Status     string
	OutputTail string
	Result     string
	IsError    bool
}

type turnState struct {
	asm       *asm             // the live assembly, hoisted so repair can reach it
	final     *message.Message // the staged durable payload, once it exists
	states    map[string]turnTool
	committed bool

	// assistantOracle/toolsOracle are written only by the equivalence oracle
	// in turn_shadow_test.go.
	assistantOracle message.Message
	toolsOracle     []turnTool
}

func newTurnState() *turnState {
	return &turnState{states: map[string]turnTool{}}
}

// repairView materializes what the repair needs: the aborted assistant
// message and its tools, with outcomes folded in. Called on failure, not per
// event.
func (t *turnState) repairView() (message.Message, []turnTool) {
	var src *message.Message
	switch {
	case t.final != nil:
		src = t.final
	case t.asm != nil:
		src = t.asm.message()
	}
	assistant := partialAssistant(src)
	tools := toolsFromAssistant(assistant)
	for i := range tools {
		if state, ok := t.states[tools[i].ToolCallID]; ok {
			tools[i] = state
		}
	}
	return assistant, tools
}

// stageAssistant records the durable candidate. A failed append must preserve
// what the provider produced, not what the drain assembled.
func (a *Agent) stageAssistant(e *store.Entry[message.Message]) {
	if a.turn == nil || e == nil {
		return
	}
	a.turn.final = &e.Payload
}

func (a *Agent) noteTool(id, name, status string, isErr bool, terminalText ...string) {
	if a.turn == nil {
		return
	}
	t := a.turn.states[id]
	t.ToolCallID = id
	if name != "" {
		t.ToolName = name
	}
	if status != "" {
		t.Status = status
	}
	t.IsError = isErr
	if len(terminalText) > 0 {
		terminal := strings.TrimSpace(terminalText[0])
		streamed := ""
		if a.gov != nil {
			streamed = strings.TrimSpace(a.gov.Tails()[id])
		}
		var combined string
		switch {
		case terminal == "":
			combined = streamed
		case streamed == "", strings.Contains(terminal, streamed):
			combined = terminal
		case strings.Contains(streamed, terminal):
			combined = streamed
		default:
			combined = streamed + "\n" + terminal
		}
		t.Result = combined
		t.OutputTail = boundedToolTail(combined)
	} else if a.gov != nil {
		t.OutputTail = a.gov.Tails()[id]
	}
	a.turn.states[id] = t
}

func (a *Agent) repairTurnTail() ([]message.Message, error) {
	t := a.turn
	a.turn = nil
	if t == nil {
		return nil, nil
	}
	assistant, turnTools := t.repairView()
	if t.committed {
		tools := interruptedToolResults(turnTools)
		if len(tools) == 0 {
			return nil, nil
		}
		e, err := a.appendMsg(message.Message{
			Role: message.RoleInput, Content: tools, Timestamp: time.Now().UnixMilli(),
		})
		if err != nil {
			return nil, fmt.Errorf("repair interrupted tool results: %w", err)
		}
		return []message.Message{e.Payload}, nil
	}
	assistant.Role = message.RoleOutput
	assistant.StopReason = message.StopAborted
	if len(assistant.Content) == 0 {
		return nil, nil
	}
	if assistant.Timestamp == 0 {
		assistant.Timestamp = time.Now().UnixMilli()
	}
	e, err := a.appendMsg(assistant)
	if err != nil {
		return nil, fmt.Errorf("repair interrupted assistant: %w", err)
	}
	appended := []message.Message{e.Payload}
	if tools := interruptedToolResults(turnTools); len(tools) > 0 {
		e, err := a.appendMsg(message.Message{
			Role: message.RoleInput, Content: tools, Timestamp: time.Now().UnixMilli(),
		})
		if err != nil {
			return appended, fmt.Errorf("repair interrupted tool results: %w", err)
		}
		appended = append(appended, e.Payload)
	}
	return appended, nil
}

func (a *Agent) commitAssistantCache(lt uint64, cache *provider.AssistantCache) error {
	if cache == nil {
		return nil
	}
	if cache.Namespace == "" {
		return fmt.Errorf("provider assistant cache namespace is empty")
	}
	native, err := a.backend.OpenTranslator(a.id, cache.Namespace)
	if err != nil {
		return fmt.Errorf("open assistant cache %s: %w", cache.Namespace, err)
	}
	if _, err := native.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    lt,
		Payload:     cloneRawMessages(cache.Payload),
		Fingerprint: cache.Fingerprint,
	}); err != nil {
		return fmt.Errorf("append assistant cache %s at LT %d: %w", cache.Namespace, lt, err)
	}
	return nil
}

func partialAssistant(m *message.Message) message.Message {
	out := message.Message{Role: message.RoleOutput}
	if m == nil {
		return out
	}
	out.Timestamp = m.Timestamp
	out.Usage = m.Usage
	out.StopReason = m.StopReason
	for _, c := range m.Content {
		if c.Type == message.ContentToolInvoke {
			if c.ToolCallID == "" || c.ToolName == "" || c.Arguments == nil {
				continue
			}
		}
		out.Content = append(out.Content, c)
	}
	return out
}

func toolsFromAssistant(m message.Message) []turnTool {
	var out []turnTool
	for _, c := range m.Content {
		if c.Type != message.ContentToolInvoke || c.ToolCallID == "" {
			continue
		}
		out = append(out, turnTool{
			ToolCallID: c.ToolCallID,
			ToolName:   c.ToolName,
			Status:     "pending",
		})
	}
	return out
}

func interruptedToolResults(tools []turnTool) []message.Content {
	out := make([]message.Content, 0, len(tools))
	for _, tool := range tools {
		if tool.ToolCallID == "" {
			continue
		}
		text := strings.TrimSpace(tool.OutputTail)
		if tool.Status == "ok" || tool.Status == "error" {
			if full := strings.TrimSpace(tool.Result); full != "" {
				text = full
			} else if text != "" {
				text += "\n\n[output truncated: process interrupted before the full result was recorded]"
			}
			out = append(out, message.ToolResultContent(
				tool.ToolCallID, tool.ToolName, text, tool.Status == "error" || tool.IsError,
			))
			continue
		}
		if text != "" {
			text += "\n\n"
		}
		text += InterruptedToolNotice
		out = append(out, message.ToolResultContent(tool.ToolCallID, tool.ToolName, text, true))
	}
	return out
}

func boundedToolTail(text string) string {
	const maxBytes = 64 << 10
	lines := strings.Split(text, "\n")
	if len(lines) > liveOutputTail {
		lines = lines[len(lines)-liveOutputTail:]
	}
	text = strings.Join(lines, "\n")
	if len(text) > maxBytes {
		text = text[len(text)-maxBytes:]
	}
	return text
}

func cloneRawMessages(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i := range in {
		out[i] = append(json.RawMessage(nil), in[i]...)
	}
	return out
}
