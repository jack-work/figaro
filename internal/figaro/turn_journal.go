package figaro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

const (
	turnCheckpointVersion  = 2
	turnCheckpointInterval = time.Second
	interruptedToolNotice  = "interrupted: tool execution did not complete"
)

type turnPhase string

const (
	turnPhaseAssistant turnPhase = "assistant"
	turnPhaseTools     turnPhase = "tools"
)

type turnCheckpoint struct {
	Version     int                   `json:"version"`
	TurnID      string                `json:"turn_id"`
	Generation  uint64                `json:"generation"`
	TargetLT    uint64                `json:"target_next_ir_lt"`
	Phase       turnPhase             `json:"phase"`
	Assistant   message.Message       `json:"partial_assistant"`
	Commit      *turnCheckpointCommit `json:"commit,omitempty"`
	Tools       []turnCheckpointTool  `json:"tools,omitempty"`
	TimestampMS int64                 `json:"timestamp_ms"`
}

type turnCheckpointCommit struct {
	AssistantLT uint64               `json:"assistant_lt"`
	Cache       *turnCheckpointCache `json:"cache,omitempty"`
}

type turnCheckpointCache struct {
	Namespace   string            `json:"namespace"`
	Payload     []json.RawMessage `json:"payload"`
	Fingerprint string            `json:"fingerprint"`
}

type turnCheckpointTool struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Status     string `json:"status"`
	OutputTail string `json:"output_tail,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

type activeTurn struct {
	checkpoint         turnCheckpoint
	toolStates         map[string]turnCheckpointTool
	lastSync           time.Time
	durable            *turnCheckpoint
	assistantCommitted bool
}

func (a *Agent) openTurnJournal() {
	a.turnJournal = nil
	a.journalErr = nil
	if a.backend == nil {
		return
	}
	j, err := a.backend.OpenTurnJournal(a.id)
	if err != nil {
		a.journalErr = err
		return
	}
	a.turnJournal = j
}

func (a *Agent) beginTurnCheckpoint(targetLT uint64) error {
	if a.turnJournal == nil {
		a.activeTurn = nil
		return a.journalErr
	}
	a.turnGeneration++
	now := time.Now()
	a.activeTurn = &activeTurn{
		checkpoint: turnCheckpoint{
			Version:     turnCheckpointVersion,
			TurnID:      fmt.Sprintf("%s-%d", a.id, now.UnixNano()),
			Generation:  a.turnGeneration,
			TargetLT:    targetLT,
			Phase:       turnPhaseAssistant,
			Assistant:   message.Message{Role: message.RoleAssistant},
			TimestampMS: now.UnixMilli(),
		},
		toolStates: make(map[string]turnCheckpointTool),
		lastSync:   now,
	}
	return a.writeTurnCheckpoint(true, false)
}

func (a *Agent) updateAssistantCheckpoint(m *message.Message) {
	if a.activeTurn == nil {
		return
	}
	a.activeTurn.checkpoint.Phase = turnPhaseAssistant
	a.activeTurn.checkpoint.Assistant = checkpointAssistant(m)
	a.activeTurn.checkpoint.TimestampMS = time.Now().UnixMilli()
	a.activeTurn.checkpoint.Tools = mergeCheckpointTools(
		checkpointToolsFromAssistant(a.activeTurn.checkpoint.Assistant),
		a.activeTurn.checkpoint.Tools,
		a.activeTurn.toolStates,
	)
}

func (a *Agent) beginToolsCheckpoint(assistant message.Message, targetLT uint64) {
	if a.activeTurn == nil {
		return
	}
	a.activeTurn.checkpoint.Phase = turnPhaseTools
	a.activeTurn.checkpoint.TargetLT = targetLT
	a.activeTurn.checkpoint.Assistant = checkpointAssistant(&assistant)
	a.activeTurn.checkpoint.Tools = mergeCheckpointTools(
		checkpointToolsFromAssistant(a.activeTurn.checkpoint.Assistant),
		a.activeTurn.checkpoint.Tools,
		a.activeTurn.toolStates,
	)
	a.activeTurn.checkpoint.TimestampMS = time.Now().UnixMilli()
}

func (a *Agent) stageAssistantCommit(assistant message.Message, assistantLT uint64, cache *provider.AssistantCache) error {
	if a.activeTurn == nil {
		return fmt.Errorf("turn checkpoint is not active")
	}
	a.activeTurn.checkpoint.Assistant = checkpointAssistant(&assistant)
	a.activeTurn.checkpoint.TargetLT = assistantLT + 1
	commit := &turnCheckpointCommit{AssistantLT: assistantLT}
	if cache != nil {
		if cache.Namespace == "" {
			return fmt.Errorf("provider assistant cache namespace is empty")
		}
		commit.Cache = &turnCheckpointCache{
			Namespace:   cache.Namespace,
			Payload:     cloneRawMessages(cache.Payload),
			Fingerprint: cache.Fingerprint,
		}
	}
	a.activeTurn.checkpoint.Commit = commit
	a.activeTurn.checkpoint.TimestampMS = time.Now().UnixMilli()
	return nil
}

func (a *Agent) updateToolCheckpoint(id, name, status string, isErr bool, terminalText ...string) {
	if a.activeTurn == nil {
		return
	}
	t := a.activeTurn.toolStates[id]
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
		switch {
		case terminal == "":
			t.OutputTail = boundedToolTail(streamed)
		case streamed == "", strings.Contains(terminal, streamed):
			t.OutputTail = boundedToolTail(terminal)
		case strings.Contains(streamed, terminal):
			t.OutputTail = boundedToolTail(streamed)
		default:
			t.OutputTail = boundedToolTail(streamed + "\n" + terminal)
		}
	} else if a.gov != nil {
		t.OutputTail = a.gov.Tails()[id]
	}
	a.activeTurn.toolStates[id] = t
	for i := range a.activeTurn.checkpoint.Tools {
		if a.activeTurn.checkpoint.Tools[i].ToolCallID == id {
			a.activeTurn.checkpoint.Tools[i] = t
			break
		}
	}
	a.activeTurn.checkpoint.TimestampMS = time.Now().UnixMilli()
}

func checkpointAssistant(m *message.Message) message.Message {
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

func checkpointToolsFromAssistant(m message.Message) []turnCheckpointTool {
	var out []turnCheckpointTool
	for _, c := range m.Content {
		if c.Type != message.ContentToolInvoke || c.ToolCallID == "" || c.Arguments == nil {
			continue
		}
		out = append(out, turnCheckpointTool{
			ToolCallID: c.ToolCallID,
			ToolName:   c.ToolName,
			Status:     "pending",
		})
	}
	return out
}

func mergeCheckpointTools(current, previous []turnCheckpointTool, states map[string]turnCheckpointTool) []turnCheckpointTool {
	byID := make(map[string]turnCheckpointTool, len(previous))
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

func (a *Agent) writeTurnCheckpoint(structural, forceSync bool) error {
	if a.activeTurn == nil || a.turnJournal == nil {
		if a.journalErr != nil {
			return a.journalErr
		}
		return nil
	}
	now := time.Now()
	periodic := !structural
	if periodic && now.Sub(a.activeTurn.lastSync) < turnCheckpointInterval {
		return nil
	}
	a.activeTurn.checkpoint.TimestampMS = now.UnixMilli()
	payload, err := json.Marshal(a.activeTurn.checkpoint)
	if err != nil {
		return err
	}
	if err := a.turnJournal.Checkpoint(a.activeTurn.checkpoint.TargetLT, payload); err != nil {
		return fmt.Errorf("turn checkpoint: %w", err)
	}
	if forceSync || now.Sub(a.activeTurn.lastSync) >= turnCheckpointInterval {
		if err := a.turnJournal.Sync(); err != nil {
			return fmt.Errorf("turn checkpoint sync: %w", err)
		}
		a.activeTurn.lastSync = now
		snapshot := a.activeTurn.checkpoint
		a.activeTurn.durable = &snapshot
	}
	return nil
}

func (a *Agent) forceTurnCheckpoint() error {
	return a.writeTurnCheckpoint(true, true)
}

func (a *Agent) completeCheckpointCommit(cp turnCheckpoint) ([]message.Message, error) {
	if cp.Commit == nil {
		return nil, nil
	}
	lt := cp.Commit.AssistantLT
	if lt == 0 {
		return nil, fmt.Errorf("turn checkpoint commit assistant LT is zero")
	}
	var appended []message.Message
	existing, ok := a.figLog.Lookup(lt)
	if ok {
		want, got := cp.Assistant, existing.Payload
		want.LogicalTime, got.LogicalTime = 0, 0
		if !reflect.DeepEqual(want, got) {
			return nil, fmt.Errorf("turn checkpoint assistant differs from canonical IR at LT %d", lt)
		}
	} else {
		if a.nextIndex() != lt {
			return nil, fmt.Errorf("turn checkpoint assistant LT %d is not next IR LT %d", lt, a.nextIndex())
		}
		entry, err := a.figLog.Append(store.Entry[message.Message]{Payload: cp.Assistant})
		if err != nil {
			return nil, fmt.Errorf("commit checkpoint assistant: %w", err)
		}
		if entry.LT != lt || entry.FigaroLT != lt {
			return nil, fmt.Errorf("assistant seal LT mismatch: predicted %d, got lt=%d main_lt=%d", lt, entry.LT, entry.FigaroLT)
		}
		appended = append(appended, entry.Payload)
	}
	if cp.Commit.Cache == nil {
		return appended, nil
	}
	if a.backend == nil {
		return appended, fmt.Errorf("turn checkpoint has provider cache without a backend")
	}
	native, err := a.backend.OpenTranslation(a.id, cp.Commit.Cache.Namespace)
	if err != nil {
		return appended, fmt.Errorf("open assistant cache %s: %w", cp.Commit.Cache.Namespace, err)
	}
	if cached, ok := native.Lookup(lt); ok {
		if cached.Fingerprint != cp.Commit.Cache.Fingerprint ||
			!rawMessagesEqual(cached.Payload, cp.Commit.Cache.Payload) {
			return appended, fmt.Errorf("assistant cache differs at LT %d in %s", lt, cp.Commit.Cache.Namespace)
		}
		if err := a.backend.SyncTranslation(a.id, cp.Commit.Cache.Namespace); err != nil {
			return appended, fmt.Errorf("sync assistant cache %s at LT %d: %w", cp.Commit.Cache.Namespace, lt, err)
		}
		return appended, nil
	}
	if _, err := native.Append(store.Entry[[]json.RawMessage]{
		FigaroLT:    lt,
		Payload:     cloneRawMessages(cp.Commit.Cache.Payload),
		Fingerprint: cp.Commit.Cache.Fingerprint,
	}); err != nil {
		return appended, fmt.Errorf("append assistant cache %s at LT %d: %w", cp.Commit.Cache.Namespace, lt, err)
	}
	if err := a.backend.SyncTranslation(a.id, cp.Commit.Cache.Namespace); err != nil {
		return appended, fmt.Errorf("sync assistant cache %s at LT %d: %w", cp.Commit.Cache.Namespace, lt, err)
	}
	return appended, nil
}

func cloneRawMessages(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(in))
	for i := range in {
		out[i] = append(json.RawMessage(nil), in[i]...)
	}
	return out
}

func rawMessagesEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (a *Agent) recoverPendingTurn() ([]message.Message, error) {
	if a.journalErr != nil {
		return nil, a.journalErr
	}
	if a.turnJournal == nil {
		return nil, nil
	}
	target := uint64(1)
	if tail, ok := a.figLog.PeekTail(); ok {
		target = tail.FigaroLT + 1
	}
	payload, ok, err := a.turnJournal.Latest(target)
	if err != nil || !ok {
		return nil, err
	}
	var cp turnCheckpoint
	if err := json.Unmarshal(payload, &cp); err != nil {
		return nil, fmt.Errorf("decode turn checkpoint: %w", err)
	}
	if (cp.Version != 1 && cp.Version != turnCheckpointVersion) || cp.TargetLT != target {
		if (cp.Version != 1 && cp.Version != turnCheckpointVersion) ||
			cp.Phase != turnPhaseTools ||
			cp.TargetLT != target+1 {
			if cp.Commit == nil || cp.Commit.AssistantLT != target || cp.TargetLT != target+1 {
				return nil, nil
			}
		}
		if cp.Commit == nil {
			cp.Phase = turnPhaseAssistant
			cp.TargetLT = target
		}
	}
	messages, err := a.sealCheckpoint(cp)
	if err != nil {
		return messages, err
	}
	if err := a.retireTurnJournal(); err != nil {
		return messages, fmt.Errorf("retire recovered turn journal: %w", err)
	}
	return messages, nil
}

func (a *Agent) sealActiveTurn() ([]message.Message, error) {
	if a.activeTurn == nil {
		return nil, nil
	}
	cp := a.activeTurn.checkpoint
	assistantCommitted := a.activeTurn.assistantCommitted
	if err := a.forceTurnCheckpoint(); err != nil {
		if assistantCommitted && cp.Phase == turnPhaseTools {
			// Canonical assistant IR is already durable; seal its tools from
			// actor memory even when the journal durability point failed.
		} else if a.activeTurn.durable == nil {
			return nil, err
		} else {
			cp = *a.activeTurn.durable
		}
	}
	if cp.Phase == turnPhaseTools && !assistantCommitted {
		cp.Phase = turnPhaseAssistant
		if cp.TargetLT > 0 {
			cp.TargetLT--
		}
	}
	msgs, err := a.sealCheckpoint(cp)
	a.activeTurn = nil
	if err == nil {
		if retireErr := a.retireTurnJournal(); retireErr != nil {
			err = fmt.Errorf("retire turn journal: %w", retireErr)
		}
	}
	return msgs, err
}

func (a *Agent) retireTurnJournal() error {
	if a.turnJournal == nil {
		return nil
	}
	return a.turnJournal.Retire()
}

func (a *Agent) sealCheckpoint(cp turnCheckpoint) ([]message.Message, error) {
	var appended []message.Message
	if cp.Commit != nil {
		committed, err := a.completeCheckpointCommit(cp)
		appended = append(appended, committed...)
		if err != nil {
			return appended, err
		}
		if tools := interruptedToolResults(cp.Tools); len(tools) > 0 {
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
	switch cp.Phase {
	case turnPhaseAssistant:
		assistant := cp.Assistant
		assistant.Role = message.RoleAssistant
		assistant.StopReason = message.StopAborted
		if assistant.Timestamp == 0 {
			assistant.Timestamp = cp.TimestampMS
		}
		if assistant.Timestamp == 0 {
			assistant.Timestamp = time.Now().UnixMilli()
		}
		handoff := cp
		handoff.Phase = turnPhaseTools
		handoff.TargetLT = cp.TargetLT + 1
		handoff.Assistant = assistant
		handoff.TimestampMS = time.Now().UnixMilli()
		payload, err := json.Marshal(handoff)
		if err != nil {
			return nil, fmt.Errorf("encode recovered tool handoff: %w", err)
		}
		if err := a.turnJournal.Checkpoint(handoff.TargetLT, payload); err != nil {
			return nil, fmt.Errorf("checkpoint recovered tool handoff: %w", err)
		}
		if err := a.turnJournal.Sync(); err != nil {
			return nil, fmt.Errorf("sync recovered tool handoff: %w", err)
		}
		e, err := a.figLog.Append(store.Entry[message.Message]{Payload: assistant})
		if err != nil {
			return nil, fmt.Errorf("seal interrupted assistant: %w", err)
		}
		appended = append(appended, e.Payload)
		if tools := interruptedToolResults(cp.Tools); len(tools) > 0 {
			e, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
				Role: message.RoleUser, Content: tools, Timestamp: time.Now().UnixMilli(),
			}})
			if err != nil {
				return appended, fmt.Errorf("seal interrupted tool results: %w", err)
			}
			appended = append(appended, e.Payload)
		}
	case turnPhaseTools:
		tools := interruptedToolResults(cp.Tools)
		if len(tools) == 0 {
			return nil, nil
		}
		e, err := a.figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleUser, Content: tools, Timestamp: time.Now().UnixMilli(),
		}})
		if err != nil {
			return nil, fmt.Errorf("seal interrupted tool results: %w", err)
		}
		appended = append(appended, e.Payload)
	default:
		return nil, fmt.Errorf("unknown turn checkpoint phase %q", cp.Phase)
	}
	return appended, nil
}

func interruptedToolResults(tools []turnCheckpointTool) []message.Content {
	out := make([]message.Content, 0, len(tools))
	for _, tool := range tools {
		if tool.ToolCallID == "" {
			continue
		}
		text := strings.TrimSpace(tool.OutputTail)
		if tool.Status == "ok" || tool.Status == "error" {
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
