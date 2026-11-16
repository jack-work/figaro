package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

const (
	skipThoughtSigValidator = "skip_thought_signature_validator"
	unclosedCallNotice      = "tool call was never closed; the turn ended first"
	defaultMaxOutputTokens  = 65536
)

var reqSeq atomic.Uint64

type CodeAssistConfig struct {
	Project     string
	UserAgent   string
	RequestType string
}

type Blob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type FuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
	ID   string         `json:"id,omitempty"`
}

type FuncResponse struct {
	Name     string         `json:"name"`
	ID       string         `json:"id,omitempty"`
	Response map[string]any `json:"response"`
}

type Part struct {
	Text             string        `json:"text,omitempty"`
	Thought          bool          `json:"thought,omitempty"`
	ThoughtSignature string        `json:"thoughtSignature,omitempty"`
	InlineData       *Blob         `json:"inlineData,omitempty"`
	FunctionCall     *FuncCall     `json:"functionCall,omitempty"`
	FunctionResponse *FuncResponse `json:"functionResponse,omitempty"`
}

type Content struct {
	Role       string `json:"role"`
	Parts      []Part `json:"parts,omitempty"`
	SystemText string `json:"systemText,omitempty"`
}

type FuncDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type ToolGroup struct {
	FunctionDeclarations []FuncDecl `json:"functionDeclarations"`
}

type ThinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  int  `json:"thinkingBudget,omitempty"`
}

type GenerationConfig struct {
	MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"topP,omitempty"`
	ThinkingConfig  *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

type SystemInstruction struct {
	Parts []Part `json:"parts"`
}

type GenerateRequest struct {
	SystemInstruction *SystemInstruction `json:"systemInstruction,omitempty"`
	Contents          []json.RawMessage  `json:"contents"`
	Tools             []ToolGroup        `json:"tools,omitempty"`
	GenerationConfig  GenerationConfig   `json:"generationConfig"`
}

type CodeAssistRequest struct {
	Model       string          `json:"model"`
	Project     string          `json:"project,omitempty"`
	UserAgent   string          `json:"userAgent,omitempty"`
	RequestType string          `json:"requestType,omitempty"`
	RequestID   string          `json:"requestId"`
	Request     json.RawMessage `json:"request"`
}

func encodeMessage(msg message.Message, reminders []string) ([]Content, error) {
	switch msg.Role {
	case message.RoleGenesis:
		if len(reminders) == 0 {
			return nil, nil
		}
		var parts []Part
		for _, r := range reminders {
			if r != "" {
				parts = append(parts, Part{Text: r})
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []Content{{Role: "user", Parts: parts}}, nil

	case message.RoleSystem:
		var text string
		for _, c := range msg.Content {
			if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
				text = joinText(text, c.Text)
			}
		}
		if text == "" {
			return nil, nil
		}
		return []Content{{Role: "system", SystemText: text}}, nil

	case message.RoleInput, message.RoleToolResult:
		var parts []Part
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentToolResult:
				if c.ToolCallID == "" {
					continue
				}
				var respObj map[string]any
				if c.IsError {
					txt := c.Text
					if txt == "" {
						txt = "tool failed"
					}
					respObj = map[string]any{"error": txt, "is_error": true}
				} else {
					respObj = map[string]any{"output": c.Text}
				}
				parts = append(parts, Part{
					FunctionResponse: &FuncResponse{
						ID:       c.ToolCallID,
						Name:     c.ToolName,
						Response: respObj,
					},
				})
			case message.ContentImage:
				if c.Data == "" {
					continue
				}
				mime := c.MimeType
				if mime == "" {
					mime = "image/png"
				}
				parts = append(parts, Part{
					InlineData: &Blob{MimeType: mime, Data: c.Data},
				})
			case message.ContentProse:
				if c.Text == "" {
					continue
				}
				if sr := provider.SenderReminder(c.Sender); sr != "" {
					parts = append(parts, Part{Text: sr})
				}
				parts = append(parts, Part{Text: c.Text})
			}
		}
		parts = placeReminders(parts, reminders)
		parts = resultsFirstParts(parts)
		if len(parts) == 0 {
			return nil, nil
		}
		return []Content{{Role: "user", Parts: parts}}, nil

	case message.RoleSystemInterrupt:
		var parts []Part
		for _, c := range msg.Content {
			if c.Type != message.ContentInterrupt || c.ToolCallID == "" {
				continue
			}
			txt := c.Text
			if txt == "" {
				txt = "(tool execution was interrupted)"
			}
			parts = append(parts, Part{
				FunctionResponse: &FuncResponse{
					ID:   c.ToolCallID,
					Name: c.ToolName,
					Response: map[string]any{
						"error":       txt,
						"is_error":    true,
						"interrupted": true,
					},
				},
			})
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []Content{{Role: "user", Parts: parts}}, nil

	case message.RoleOutput:
		var parts []Part
		for _, c := range msg.Content {
			switch c.Type {
			case message.ContentThinking:
				continue
			case message.ContentProse:
				if strings.TrimSpace(c.Text) != "" || c.Signature != "" {
					parts = append(parts, Part{
						Text:             c.Text,
						ThoughtSignature: c.Signature,
					})
				}
			case message.ContentToolInvoke:
				if c.ToolCallID == "" || c.ToolName == "" {
					continue
				}
				if _, bad := message.MalformedArgsOf(c); bad {
					continue
				}
				args := c.Arguments
				if args == nil {
					args = map[string]any{}
				}
				sig := c.Signature
				if sig == "" {
					sig = skipThoughtSigValidator
				}
				parts = append(parts, Part{
					FunctionCall: &FuncCall{
						ID:   c.ToolCallID,
						Name: c.ToolName,
						Args: args,
					},
					ThoughtSignature: sig,
				})
			}
		}
		if len(parts) == 0 {
			return nil, nil
		}
		return []Content{{Role: "model", Parts: parts}}, nil
	}
	return nil, nil
}

func placeReminders(parts []Part, reminders []string) []Part {
	var clean []string
	for _, r := range reminders {
		if r != "" {
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return parts
	}
	hasHumanInput := false
	lastResp := -1
	for i, p := range parts {
		if p.FunctionResponse != nil {
			lastResp = i
			continue
		}
		if p.Text != "" || p.InlineData != nil {
			hasHumanInput = true
		}
	}
	if hasHumanInput || lastResp < 0 {
		for _, r := range clean {
			parts = append(parts, Part{Text: r})
		}
		return parts
	}
	joined := strings.Join(clean, "\n\n")
	resp := parts[lastResp].FunctionResponse.Response
	if resp == nil {
		resp = map[string]any{}
		parts[lastResp].FunctionResponse.Response = resp
	}
	if outStr, ok := resp["output"].(string); ok {
		resp["output"] = joinText(outStr, joined)
	} else if errStr, ok := resp["error"].(string); ok {
		resp["error"] = joinText(errStr, joined)
	} else {
		resp["reminders"] = joined
	}
	return parts
}

func resultsFirstParts(parts []Part) []Part {
	sawNonResp := false
	needsReorder := false
	for _, p := range parts {
		if p.FunctionResponse == nil {
			sawNonResp = true
		} else if sawNonResp {
			needsReorder = true
			break
		}
	}
	if !needsReorder {
		return parts
	}
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		if p.FunctionResponse != nil {
			out = append(out, p)
		}
	}
	for _, p := range parts {
		if p.FunctionResponse == nil {
			out = append(out, p)
		}
	}
	return out
}

func pairAndCollectSystem(rows provider.RowSeq, baseCredo string) (provider.RowSeq, *SystemInstruction) {
	var sysParts []Part
	if strings.TrimSpace(baseCredo) != "" {
		sysParts = append(sysParts, Part{Text: baseCredo})
	}
	for raw := range rows {
		var probe struct {
			Role       string `json:"role"`
			SystemText string `json:"systemText"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Role == "system" && strings.TrimSpace(probe.SystemText) != "" {
			sysParts = append(sysParts, Part{Text: probe.SystemText})
		}
	}
	var sys *SystemInstruction
	if len(sysParts) > 0 {
		sys = &SystemInstruction{Parts: sysParts}
	}

	paired := func(yield func(json.RawMessage, uint64) bool) {
		var (
			open       []FuncCall
			toolNames  = map[string]string{}
			seenResIDs = map[string]bool{}
			pending    *Content
			pendingLT  uint64
			emittedAny bool
		)

		emit := func(c Content, lt uint64) bool {
			if len(c.Parts) == 0 {
				return true
			}
			if !emittedAny && c.Role == "model" {
				lead, _ := json.Marshal(Content{Role: "user", Parts: []Part{{Text: "."}}})
				if !yield(lead, lt) {
					return false
				}
				emittedAny = true
			}
			b, err := json.Marshal(c)
			if err != nil {
				return true
			}
			emittedAny = true
			return yield(b, lt)
		}

		flushPending := func() bool {
			if pending == nil {
				return true
			}
			c, lt := *pending, pendingLT
			pending = nil
			return emit(c, lt)
		}

		closeOpen := func() []Part {
			if len(open) == 0 {
				return nil
			}
			closing := make([]Part, 0, len(open))
			for _, c := range open {
				name := c.Name
				if name == "" {
					name = "tool"
				}
				closing = append(closing, Part{
					FunctionResponse: &FuncResponse{
						ID:   c.ID,
						Name: name,
						Response: map[string]any{
							"error":    unclosedCallNotice,
							"is_error": true,
						},
					},
				})
			}
			open = nil
			return closing
		}

		for raw, lt := range rows {
			var turn Content
			if json.Unmarshal(raw, &turn) != nil || turn.Role == "system" || len(turn.Parts) == 0 {
				continue
			}

			if turn.Role == "model" {
				if len(open) > 0 {
					closing := closeOpen()
					if pending != nil && pending.Role == "user" {
						pending.Parts = resultsFirstParts(append(pending.Parts, closing...))
					} else {
						if !flushPending() {
							return
						}
						pending = &Content{Role: "user", Parts: closing}
						pendingLT = lt
					}
				}
				var made []FuncCall
				for i := range turn.Parts {
					fc := turn.Parts[i].FunctionCall
					if fc != nil && fc.ID != "" {
						if fc.Name != "" {
							toolNames[fc.ID] = fc.Name
						}
						made = append(made, *fc)
						if turn.Parts[i].ThoughtSignature == "" {
							turn.Parts[i].ThoughtSignature = skipThoughtSigValidator
						}
					}
				}
				if pending != nil && pending.Role == "model" {
					pending.Parts = append(pending.Parts, turn.Parts...)
					pendingLT = lt
				} else {
					if !flushPending() {
						return
					}
					cp := turn
					pending = &cp
					pendingLT = lt
				}
				open = append(open, made...)
				continue
			}

			expected := make(map[string]FuncCall, len(open))
			for _, c := range open {
				if c.ID != "" {
					expected[c.ID] = c
				}
			}
			answered := make(map[string]bool, len(open))
			var kept []Part
			for _, p := range turn.Parts {
				if p.FunctionResponse != nil {
					id := p.FunctionResponse.ID
					if id == "" || seenResIDs[id] || len(expected) == 0 {
						continue
					}
					call, ok := expected[id]
					if !ok || answered[id] {
						continue
					}
					seenResIDs[id] = true
					if p.FunctionResponse.Name == "" {
						if call.Name != "" {
							p.FunctionResponse.Name = call.Name
						} else if n := toolNames[id]; n != "" {
							p.FunctionResponse.Name = n
						} else {
							p.FunctionResponse.Name = "tool"
						}
					}
					answered[id] = true
				}
				kept = append(kept, p)
			}

			if len(open) > 0 {
				var missing []Part
				for _, c := range open {
					if !answered[c.ID] {
						name := c.Name
						if name == "" {
							name = "tool"
						}
						missing = append(missing, Part{
							FunctionResponse: &FuncResponse{
								ID:   c.ID,
								Name: name,
								Response: map[string]any{
									"error":    unclosedCallNotice,
									"is_error": true,
								},
							},
						})
					}
				}
				if len(missing) > 0 {
					kept = append(missing, kept...)
				}
				open = nil
			}

			kept = resultsFirstParts(kept)
			if len(kept) == 0 {
				continue
			}
			if pending != nil && pending.Role == "user" {
				pending.Parts = resultsFirstParts(append(pending.Parts, kept...))
				pendingLT = lt
			} else {
				if !flushPending() {
					return
				}
				pending = &Content{Role: "user", Parts: kept}
				pendingLT = lt
			}
		}

		if len(open) > 0 {
			closing := closeOpen()
			if pending != nil && pending.Role == "user" {
				pending.Parts = resultsFirstParts(append(pending.Parts, closing...))
			} else {
				if !flushPending() {
					return
				}
				pending = &Content{Role: "user", Parts: closing}
			}
		}
		if !flushPending() {
			return
		}
		if !emittedAny {
			empty, _ := json.Marshal(Content{Role: "user", Parts: []Part{{Text: ""}}})
			yield(empty, 0)
		}
	}
	return paired, sys
}

func projectTools(tools []provider.Tool) []ToolGroup {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]FuncDecl, 0, len(tools))
	for _, t := range tools {
		if t.Name == "" {
			continue
		}
		decls = append(decls, FuncDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  NormalizeToolParameters(t.Parameters),
		})
	}
	if len(decls) == 0 {
		return nil
	}
	return []ToolGroup{{FunctionDeclarations: decls}}
}

func buildGenerationConfig(snap form.Snapshot, maxTokens int) (GenerationConfig, error) {
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}
	cfg := GenerationConfig{MaxOutputTokens: maxTokens}
	if temp, set, err := optionalFloat(snap, "system.temperature"); err != nil {
		return GenerationConfig{}, err
	} else if set {
		cfg.Temperature = &temp
	}
	if topP, set, err := optionalFloat(snap, "system.top_p"); err != nil {
		return GenerationConfig{}, err
	} else if set {
		cfg.TopP = &topP
	}
	if raw, ok := snap.Get("system.thinking_budget"); ok && string(raw) != "null" {
		var budget int
		if json.Unmarshal(raw, &budget) != nil {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				budget, _ = strconv.Atoi(strings.TrimSpace(s))
			}
		}
		if budget > 0 {
			cfg.ThinkingConfig = &ThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  budget,
			}
		}
	}
	return cfg, nil
}

func optionalFloat(snap form.Snapshot, key string) (float64, bool, error) {
	raw, ok := snap.Get(key)
	if !ok || string(raw) == "null" {
		return 0, false, nil
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false, fmt.Errorf("gemini: %s must be a number: %w", key, err)
	}
	return v, true, nil
}

func readCredo(snapshot form.Snapshot) string {
	raw, ok := snapshot.Get("system.credo")
	if !ok {
		return ""
	}
	var env struct {
		Content     string `json:"content,omitempty"`
		Frontmatter string `json:"frontmatter,omitempty"`
	}
	if json.Unmarshal(raw, &env) == nil && (env.Content != "" || env.Frontmatter != "") {
		if env.Content != "" {
			return env.Content
		}
		return env.Frontmatter
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func joinText(acc, add string) string {
	if add == "" {
		return acc
	}
	if acc == "" {
		return add
	}
	return acc + "\n\n" + add
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return strings.ReplaceAll(s, "<", "&lt;")
}

func bodyFuncGemini(frame GenerateRequest, rows provider.RowSeq) provider.BodyFunc {
	frame.Contents = nil
	return func(w io.Writer) error {
		return provider.WriteJSONWithRowSeq(w, frame, "contents", rows)
	}
}

func bodyFuncCodeAssist(model string, ca CodeAssistConfig, frame GenerateRequest, rows provider.RowSeq) provider.BodyFunc {
	reqID := fmt.Sprintf("agent-%d-%d", time.Now().UnixNano(), reqSeq.Add(1))
	return func(w io.Writer) error {
		var prefix bytes.Buffer
		prefix.WriteByte('{')
		writeField := func(k, v string) {
			kb, _ := json.Marshal(k)
			vb, _ := json.Marshal(v)
			prefix.Write(kb)
			prefix.WriteByte(':')
			prefix.Write(vb)
			prefix.WriteByte(',')
		}
		writeField("model", model)
		if ca.Project != "" {
			writeField("project", ca.Project)
		}
		if ca.UserAgent != "" {
			writeField("userAgent", ca.UserAgent)
		}
		if ca.RequestType != "" {
			writeField("requestType", ca.RequestType)
		}
		writeField("requestId", reqID)
		prefix.WriteString(`"request":`)
		if _, err := w.Write(prefix.Bytes()); err != nil {
			return err
		}
		if err := bodyFuncGemini(frame, rows)(w); err != nil {
			return err
		}
		_, err := w.Write([]byte{'}'})
		return err
	}
}
