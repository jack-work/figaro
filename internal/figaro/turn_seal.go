package figaro

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

const interruptedToolNotice = "interrupted: tool execution did not complete"

type turnTool struct {
	ToolCallID string
	ToolName   string
	Status     string
	OutputTail string
	Result     string
	IsError    bool
}

type turnState struct {
	assistant message.Message
	tools     []turnTool
	states    map[string]turnTool
	committed bool
}

func newTurnState() *turnState {
	return &turnState{
		assistant: message.Message{Role: message.RoleAssistant},
		states:    map[string]turnTool{},
	}
}

func (a *Agent) noteAssistant(m *message.Message) {
	if a.turn == nil {
		return
	}
	a.turn.assistant = partialAssistant(m)
	a.turn.tools = mergeTurnTools(toolsFromAssistant(a.turn.assistant), a.turn.tools, a.turn.states)
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
	for i := range a.turn.tools {
		if a.turn.tools[i].ToolCallID == id {
			a.turn.tools[i] = t
			break
		}
	}
}

func (a *Agent) sealTurn() ([]message.Message, error) {
	t := a.turn
	a.turn = nil
	if t == nil {
		return nil, nil
	}
	if t.committed {
		tools := interruptedToolResults(t.tools)
		if len(tools) == 0 {
			return nil, nil
		}
		e, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleUser, Content: tools, Timestamp: time.Now().UnixMilli(),
		}})
		if err != nil {
			return nil, fmt.Errorf("seal interrupted tool results: %w", err)
		}
		return []message.Message{e.Payload}, nil
	}
	assistant := t.assistant
	assistant.Role = message.RoleAssistant
	assistant.StopReason = message.StopAborted
	if len(assistant.Content) == 0 {
		return nil, nil
	}
	if assistant.Timestamp == 0 {
		assistant.Timestamp = time.Now().UnixMilli()
	}
	e, err := a.figLog.Append(store.Entry[message.Message]{Payload: assistant})
	if err != nil {
		return nil, fmt.Errorf("seal interrupted assistant: %w", err)
	}
	appended := []message.Message{e.Payload}
	if tools := interruptedToolResults(t.tools); len(tools) > 0 {
		e, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleUser, Content: tools, Timestamp: time.Now().UnixMilli(),
		}})
		if err != nil {
			return appended, fmt.Errorf("seal interrupted tool results: %w", err)
		}
		appended = append(appended, e.Payload)
	}
	return appended, nil
}

func (a *Agent) commitAssistantCache(lt uint64, cache *provider.AssistantCache) error {
	if cache == nil || a.backend == nil {
		return nil
	}
	if cache.Namespace == "" {
		return fmt.Errorf("provider assistant cache namespace is empty")
	}
	native, err := a.backend.OpenTranslation(a.id, cache.Namespace)
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
	a.backend.Kick()
	return nil
}

func partialAssistant(m *message.Message) message.Message {
	out := message.Message{Role: message.RoleAssistant}
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

func mergeTurnTools(current, previous []turnTool, states map[string]turnTool) []turnTool {
	byID := make(map[string]turnTool, len(previous))
	for _, tool := range previous {
		byID[tool.ToolCallID] = tool
	}
	for i := range current {
		if prior, ok := byID[current[i].ToolCallID]; ok {
			current[i] = prior
		}
		if state, ok := states[current[i].ToolCallID]; ok {
			current[i] = state
		}
	}
	return current
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
		text += interruptedToolNotice
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

type deferredAppendLog struct {
	base store.Log[message.Message]
	mu   sync.Mutex
	next uint64
	item *store.Entry[message.Message]
}

func newDeferredAppendLog(base store.Log[message.Message]) *deferredAppendLog {
	next := uint64(1)
	if tail, ok := base.PeekTail(); ok {
		next = tail.LT + 1
	}
	return &deferredAppendLog{base: base, next: next}
}

func (l *deferredAppendLog) Read() []store.Entry[message.Message] {
	return l.base.Read()
}
func (l *deferredAppendLog) Len() int { return l.base.Len() }
func (l *deferredAppendLog) ReadFrom(lt uint64, n int) []store.Entry[message.Message] {
	return l.base.ReadFrom(lt, n)
}
func (l *deferredAppendLog) ReadPage(from, before uint64, n int) ([]store.Entry[message.Message], int) {
	return l.base.ReadPage(from, before, n)
}
func (l *deferredAppendLog) Lookup(lt uint64) (store.Entry[message.Message], bool) {
	return l.base.Lookup(lt)
}
func (l *deferredAppendLog) PeekTail() (store.Entry[message.Message], bool) {
	return l.base.PeekTail()
}
func (l *deferredAppendLog) Clear() error { return l.base.Clear() }

func (l *deferredAppendLog) Append(e store.Entry[message.Message]) (store.Entry[message.Message], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.item != nil {
		return store.Entry[message.Message]{}, fmt.Errorf("provider appended more than one assistant message")
	}
	e.LT = l.next
	e.FigaroLT = l.next
	copy := e
	l.item = &copy
	return e, nil
}

func (l *deferredAppendLog) take(fallback message.Message) store.Entry[message.Message] {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.item != nil {
		return *l.item
	}
	return store.Entry[message.Message]{Payload: fallback}
}
