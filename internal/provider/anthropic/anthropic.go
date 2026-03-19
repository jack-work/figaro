// Package anthropic implements the figaro Provider for the Anthropic Messages API.
//
// Direct HTTP+SSE, no SDK. Converts from message.Block IR to
// Anthropic's native format, using baggage when available.
// Populates baggage on responses for cache-hit re-sends.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
)

const (
	providerName       = "anthropic"
	apiURL             = "https://api.anthropic.com/v1/messages"
	apiVersion         = "2023-06-01"
	claudeCodeVersion  = "2.1.62"
)

type Anthropic struct {
	APIKey     string
	Model      string
	IsOAuth    bool // true when using OAuth token (needs Bearer auth + Claude Code headers)
	HTTPClient *http.Client
}

func New(apiKey, model string) *Anthropic {
	isOAuth := isOAuthToken(apiKey)
	return &Anthropic{
		APIKey: apiKey, Model: model, IsOAuth: isOAuth,
		HTTPClient: &http.Client{Timeout: 10 * time.Minute},
	}
}

// isOAuthToken detects OAuth access tokens by the "sk-ant-oat" infix.
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func (a *Anthropic) Name() string { return providerName }

// --- Native types ---

type nativeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    interface{}     `json:"system,omitempty"` // string or []systemBlock for OAuth
	Messages  []nativeMessage `json:"messages"`
	Tools     []nativeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
}

// systemBlock is used for the array form of the system prompt (required for OAuth).
type systemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type nativeMessage struct {
	Role    string        `json:"role"`
	Content []nativeBlock `json:"content"`
}

type nativeBlock struct {
	Type      string      `json:"type"`
	Text      string      `json:"text,omitempty"`
	Thinking  string      `json:"thinking,omitempty"`
	Signature string      `json:"signature,omitempty"`
	ID        string      `json:"id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Input     interface{} `json:"input,omitempty"`
	ToolUseID string      `json:"tool_use_id,omitempty"`
	IsError   bool        `json:"is_error,omitempty"`
	Content   interface{} `json:"content,omitempty"`
}

type nativeTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// --- Projection: IR Block → native request ---

func (a *Anthropic) projectBlock(block *message.Block, tools []provider.Tool, maxTokens int) nativeRequest {
	req := nativeRequest{
		Model: a.Model, MaxTokens: maxTokens, Stream: true,
	}

	// Build system prompt — OAuth requires array form with Claude Code identity prefix.
	var systemText string
	if block.Header != nil && len(block.Header.Content) > 0 {
		systemText = block.Header.Content[0].Text
	}

	if a.IsOAuth {
		blocks := []systemBlock{
			{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."},
		}
		if systemText != "" {
			blocks = append(blocks, systemBlock{Type: "text", Text: systemText})
		}
		req.System = blocks
	} else if systemText != "" {
		req.System = systemText
	}

	req.Messages = a.projectMessages(block.Messages)
	req.Tools = projectTools(tools)
	return req
}

func (a *Anthropic) projectMessages(msgs []message.Message) []nativeMessage {
	var result []nativeMessage
	var pendingToolResults []nativeBlock

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		result = append(result, nativeMessage{Role: "user", Content: pendingToolResults})
		pendingToolResults = nil
	}

	for _, msg := range msgs {
		// Try baggage first
		if raw, ok := msg.Baggage[providerName]; ok {
			var cached nativeMessage
			if err := json.Unmarshal(raw, &cached); err == nil {
				if msg.Role == message.RoleToolResult {
					pendingToolResults = append(pendingToolResults, cached.Content...)
					continue
				}
				flushToolResults()
				result = append(result, cached)
				continue
			}
		}

		switch msg.Role {
		case message.RoleUser:
			flushToolResults()
			var blocks []nativeBlock
			for _, c := range msg.Content {
				switch c.Type {
				case message.ContentText:
					blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
				case message.ContentImage:
					blocks = append(blocks, nativeBlock{
						Type: "image",
						Content: map[string]interface{}{
							"type": "base64", "media_type": c.MimeType, "data": c.Data,
						},
					})
				}
			}
			if len(blocks) > 0 {
				result = append(result, nativeMessage{Role: "user", Content: blocks})
			}

		case message.RoleAssistant:
			flushToolResults()
			var blocks []nativeBlock
			for _, c := range msg.Content {
				switch c.Type {
				case message.ContentText:
					blocks = append(blocks, nativeBlock{Type: "text", Text: c.Text})
				case message.ContentThinking:
					blocks = append(blocks, nativeBlock{Type: "thinking", Thinking: c.Text})
				case message.ContentToolCall:
					blocks = append(blocks, nativeBlock{
						Type: "tool_use", ID: c.ToolCallID, Name: c.ToolName, Input: c.Arguments,
					})
				}
			}
			if len(blocks) > 0 {
				result = append(result, nativeMessage{Role: "assistant", Content: blocks})
			}

		case message.RoleToolResult:
			var contentBlocks []nativeBlock
			for _, c := range msg.Content {
				contentBlocks = append(contentBlocks, nativeBlock{Type: "text", Text: c.Text})
			}
			pendingToolResults = append(pendingToolResults, nativeBlock{
				Type: "tool_result", ToolUseID: msg.ToolCallID,
				IsError: len(msg.Content) > 0 && msg.Content[0].IsError,
				Content: contentBlocks,
			})

		case message.RoleSystem:
			// System messages from compacted headers are handled
			// at the block level, not per-message.
			continue
		}
	}
	flushToolResults()
	return result
}

func projectTools(tools []provider.Tool) []nativeTool {
	result := make([]nativeTool, len(tools))
	for i, t := range tools {
		result[i] = nativeTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters}
	}
	return result
}

// --- Send: IR Block → stream → IR Messages with baggage ---

func (a *Anthropic) Send(ctx context.Context, block *message.Block, tools []provider.Tool, maxTokens int) (<-chan provider.StreamEvent, error) {
	if maxTokens == 0 {
		maxTokens = 8192
	}

	native := a.projectBlock(block, tools, maxTokens)
	body, err := json.Marshal(native)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)

	if a.IsOAuth {
		// OAuth tokens use Bearer auth and require Claude Code impersonation headers.
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14")
		httpReq.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		httpReq.Header.Set("x-app", "cli")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		httpReq.Header.Set("x-api-key", a.APIKey)
	}

	resp, err := a.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan provider.StreamEvent, 64)
	go a.consumeSSE(resp.Body, ch)
	return ch, nil
}

func (a *Anthropic) consumeSSE(body io.ReadCloser, ch chan<- provider.StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	var (
		msg       message.Message
		usage     message.Usage
		eventType string
	)
	msg.Role = message.RoleAssistant
	msg.Provider = providerName
	msg.Model = a.Model
	msg.Timestamp = time.Now().UnixMilli()

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "content_block_start":
			var block struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id,omitempty"`
					Name string `json:"name,omitempty"`
				} `json:"content_block"`
			}
			if json.Unmarshal([]byte(data), &block) != nil {
				continue
			}
			for len(msg.Content) <= block.Index {
				msg.Content = append(msg.Content, message.Content{})
			}
			switch block.ContentBlock.Type {
			case "text":
				msg.Content[block.Index] = message.Content{Type: message.ContentText}
			case "thinking":
				msg.Content[block.Index] = message.Content{Type: message.ContentThinking}
			case "tool_use":
				msg.Content[block.Index] = message.Content{
					Type: message.ContentToolCall, ToolCallID: block.ContentBlock.ID,
					ToolName: block.ContentBlock.Name, Arguments: make(map[string]interface{}),
				}
			}

		case "content_block_delta":
			var delta struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text,omitempty"`
					Thinking    string `json:"thinking,omitempty"`
					PartialJSON string `json:"partial_json,omitempty"`
				} `json:"delta"`
			}
			if json.Unmarshal([]byte(data), &delta) != nil || delta.Index >= len(msg.Content) {
				continue
			}
			c := &msg.Content[delta.Index]
			var deltaText string
			switch delta.Delta.Type {
			case "text_delta":
				c.Text += delta.Delta.Text
				deltaText = delta.Delta.Text
			case "thinking_delta":
				c.Text += delta.Delta.Thinking
				deltaText = delta.Delta.Thinking
			case "input_json_delta":
				if c.Text == "" {
					c.Text = delta.Delta.PartialJSON
				} else {
					c.Text += delta.Delta.PartialJSON
				}
			}
			if deltaText != "" {
				ch <- provider.StreamEvent{Delta: deltaText, ContentType: c.Type, Message: &msg}
			}

		case "content_block_stop":
			var stop struct{ Index int }
			if json.Unmarshal([]byte(data), &stop) != nil || stop.Index >= len(msg.Content) {
				continue
			}
			c := &msg.Content[stop.Index]
			if c.Type == message.ContentToolCall && c.Text != "" {
				var args map[string]interface{}
				if json.Unmarshal([]byte(c.Text), &args) == nil {
					c.Arguments = args
				}
				c.Text = ""
			}
			ch <- provider.StreamEvent{ContentType: c.Type, BlockDone: true, Message: &msg}

		case "message_delta":
			var md struct {
				Delta struct{ StopReason string `json:"stop_reason"` } `json:"delta"`
				Usage struct{ OutputTokens int `json:"output_tokens"` } `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &md) == nil {
				usage.OutputTokens = md.Usage.OutputTokens
				switch md.Delta.StopReason {
				case "end_turn":
					msg.StopReason = message.StopEnd
				case "max_tokens":
					msg.StopReason = message.StopLength
				case "tool_use":
					msg.StopReason = message.StopToolUse
				}
			}

		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
						CacheRead   int `json:"cache_read_input_tokens"`
						CacheCreate int `json:"cache_creation_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(data), &ms) == nil {
				usage.InputTokens = ms.Message.Usage.InputTokens
				usage.CacheReadTokens = ms.Message.Usage.CacheRead
				usage.CacheWriteTokens = ms.Message.Usage.CacheCreate
			}

		case "message_stop":
			msg.Usage = &usage
			// Stash native form in baggage
			nativeMsg := nativeMessage{Role: "assistant"}
			for _, c := range msg.Content {
				switch c.Type {
				case message.ContentText:
					nativeMsg.Content = append(nativeMsg.Content, nativeBlock{Type: "text", Text: c.Text})
				case message.ContentThinking:
					nativeMsg.Content = append(nativeMsg.Content, nativeBlock{Type: "thinking", Thinking: c.Text})
				case message.ContentToolCall:
					nativeMsg.Content = append(nativeMsg.Content, nativeBlock{
						Type: "tool_use", ID: c.ToolCallID, Name: c.ToolName, Input: c.Arguments,
					})
				}
			}
			if raw, err := json.Marshal(nativeMsg); err == nil {
				if msg.Baggage == nil {
					msg.Baggage = make(map[string]json.RawMessage)
				}
				msg.Baggage[providerName] = raw
			}
			ch <- provider.StreamEvent{Done: true, Message: &msg}
			return

		case "error":
			var errEvt struct{ Error struct{ Message string } }
			json.Unmarshal([]byte(data), &errEvt)
			msg.StopReason = message.StopError
			ch <- provider.StreamEvent{Done: true, Message: &msg,
				Err: fmt.Errorf("anthropic stream error: %s", errEvt.Error.Message)}
			return
		}
	}

	if msg.StopReason == "" {
		msg.StopReason = message.StopAborted
		ch <- provider.StreamEvent{Done: true, Message: &msg, Err: fmt.Errorf("stream ended unexpectedly")}
	}
}
