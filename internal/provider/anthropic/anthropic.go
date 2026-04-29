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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	hush "github.com/jack-work/hush/client"
)

const (
	providerName      = "anthropic"
	apiBaseURL        = "https://api.anthropic.com/v1"
	apiMessagesURL    = apiBaseURL + "/messages"
	apiVersion        = "2023-06-01"
	claudeCodeVersion = "2.1.62"
)

type Anthropic struct {
	auth       auth.TokenResolver
	mu         sync.Mutex
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

// New creates an Anthropic provider from the config section.
// authPath is the path to the provider's auth.json for OAuth credentials.
// hushClient is the hush client for token encryption/decryption (may be nil
// if using a static API key).
// Auth is resolved internally: if APIKey is set, it's used as a
// static key. Otherwise, OAuth via hush is used. The env var
// ANTHROPIC_API_KEY is checked as a fallback before OAuth.
func New(cfg config.AnthropicProvider, authPath string, hushClient *hush.Client) (*Anthropic, error) {
	resolver, err := buildResolver(cfg.APIKey, authPath, hushClient)
	if err != nil {
		return nil, err
	}
	return &Anthropic{
		auth:       resolver,
		Model:      cfg.Model,
		MaxTokens:  cfg.MaxTokens,
		HTTPClient: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

// buildResolver returns the appropriate TokenResolver based on the
// available credentials. Priority: config api_key → env var → OAuth.
func buildResolver(apiKey, authPath string, hushClient *hush.Client) (auth.TokenResolver, error) {
	if apiKey != "" {
		return &auth.StaticKey{Key: apiKey}, nil
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return &auth.StaticKey{Key: key}, nil
	}
	if hushClient == nil {
		return nil, fmt.Errorf(
			"no API key and no hush client available.\n" +
				"  Set api_key in config, ANTHROPIC_API_KEY env, or run: figaro login",
		)
	}
	mgr := auth.NewManager(hushClient, auth.AnthropicOAuth, authPath)
	return &auth.OAuthResolver{Manager: mgr}, nil
}

// Models fetches the list of available models from the Anthropic API.
func (a *Anthropic) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiBaseURL+"/models?limit=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", apiVersion)
	if isOAuthToken(apiKey) {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		req.Header.Set("x-app", "cli")
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic models API %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	var models []provider.ModelInfo
	for _, m := range result.Data {
		models = append(models, provider.ModelInfo{
			ID:       m.ID,
			Name:     m.DisplayName,
			Provider: providerName,
		})
	}
	return models, nil
}

// isOAuthToken detects OAuth access tokens by the "sk-ant-oat" infix.
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

func (a *Anthropic) Name() string { return providerName }

// log writes to stderr with a provider prefix. Visible in the angelus log.
func log(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "anthropic: "+format+"\n", args...)
}

func (a *Anthropic) SetModel(model string) {
	a.mu.Lock()
	a.Model = model
	a.mu.Unlock()
}

// --- Native types ---

type nativeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    []systemBlock   `json:"system,omitempty"`
	Messages  []nativeMessage `json:"messages"`
	Tools     []nativeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
}

// cacheControl marks a block as a prompt-cache breakpoint. Only "ephemeral"
// is supported today; Anthropic caches the prefix up to and including the
// marked block for the cache TTL (~5 min).
type cacheControl struct {
	Type string `json:"type"`
}

// systemBlock is one entry in the system prompt array. cache_control on
// the last system block caches the full system prefix.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type nativeMessage struct {
	Role    string        `json:"role"`
	Content []nativeBlock `json:"content"`
}

type nativeBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`
	Signature    string        `json:"signature,omitempty"`
	ID           string        `json:"id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Input        interface{}   `json:"input,omitempty"`
	ToolUseID    string        `json:"tool_use_id,omitempty"`
	IsError      bool          `json:"is_error,omitempty"`
	Content      interface{}   `json:"content,omitempty"`
	Source       interface{}   `json:"source,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type nativeTool struct {
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	InputSchema  interface{}   `json:"input_schema"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// --- Projection: IR Block → native request ---

func (a *Anthropic) projectBlockWithModel(block *message.Block, tools []provider.Tool, maxTokens int, oauth bool, model string) nativeRequest {
	req := nativeRequest{
		Model: model, MaxTokens: maxTokens, Stream: true,
	}

	// Build system prompt — always array form so cache_control can attach.
	var systemText string
	if block.Header != nil && len(block.Header.Content) > 0 {
		systemText = block.Header.Content[0].Text
	}

	if oauth {
		// OAuth requires "You are Claude Code" as the first system block —
		// validated server-side for OAuth tokens.
		req.System = append(req.System, systemBlock{Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude."})
		if systemText != "" {
			// Our credo comes second, with an override instruction so the
			// model prioritizes the credo's identity.
			req.System = append(req.System, systemBlock{Type: "text", Text: "IMPORTANT: The following is your true identity and personality. " +
				"Adopt it fully. Do not identify as Claude Code — follow the persona below.\n\n" + systemText})
		}
	} else if systemText != "" {
		req.System = append(req.System, systemBlock{Type: "text", Text: systemText})
	}

	req.Messages = a.projectMessages(block.Messages)
	req.Tools = projectTools(tools)

	markCacheBreakpoints(&req)
	return req
}

// markCacheBreakpoints sets cache_control: ephemeral on the last block of
// each cacheable region of the request:
//
//  1. The last system block — caches the system prefix.
//  2. The last tool definition — caches system + tools.
//  3. The last content block of the second-to-last message — caches
//     system + tools + the conversation up through the leaf at the most
//     recent endTurn (i.e. everything that was on disk before the new
//     user prompt arrived).
//
// The third breakpoint is implicit: it follows from message ordering. If
// the conversation has fewer than two messages, no message-level
// breakpoint is set.
//
// Note: the OAuth + claude-code-20250219 beta path silently ignores
// client-controlled cache_control (verified by inspecting the
// cache_creation.ephemeral_*_input_tokens fields in the response, which
// stay at 0). On the API-key path, these breakpoints engage normally.
// The wiring is left in place so the cache works whenever the auth path
// allows it; correctness of the request bytes is verified by the unit
// tests in this package.
func markCacheBreakpoints(req *nativeRequest) {
	if n := len(req.System); n > 0 {
		req.System[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if n := len(req.Tools); n > 0 {
		req.Tools[n-1].CacheControl = &cacheControl{Type: "ephemeral"}
	}
	if n := len(req.Messages); n >= 2 {
		m := &req.Messages[n-2]
		if k := len(m.Content); k > 0 {
			m.Content[k-1].CacheControl = &cacheControl{Type: "ephemeral"}
		}
	}
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
						Source: map[string]interface{}{
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
				switch c.Type {
				case message.ContentImage:
					contentBlocks = append(contentBlocks, nativeBlock{
						Type: "image",
						Source: map[string]interface{}{
							"type": "base64", "media_type": c.MimeType, "data": c.Data,
						},
					})
				default:
					text := c.Text
					if text == "" {
						text = "(empty)"
					}
					contentBlocks = append(contentBlocks, nativeBlock{Type: "text", Text: text})
				}
			}
			if len(contentBlocks) == 0 {
				contentBlocks = []nativeBlock{{Type: "text", Text: "(empty)"}}
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
		maxTokens = a.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}

	// Snapshot model under lock (SetModel may be called between sends).
	a.mu.Lock()
	model := a.Model
	a.mu.Unlock()

	// Resolve token fresh each call (supports OAuth refresh).
	apiKey, err := a.auth.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve token: %w", err)
	}

	native := a.projectBlockWithModel(block, tools, maxTokens, isOAuthToken(apiKey), model)
	body, err := json.Marshal(native)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiMessagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)

	if isOAuthToken(apiKey) {
		// OAuth tokens use Bearer auth and require Claude Code impersonation headers.
		// prompt-caching-2024-07-31 enables client-controlled cache_control on
		// the OAuth path (otherwise it is silently ignored).
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,prompt-caching-2024-07-31")
		httpReq.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		httpReq.Header.Set("x-app", "cli")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	} else {
		httpReq.Header.Set("x-api-key", apiKey)
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
	go a.consumeSSE(resp.Body, ch, model)
	return ch, nil
}

// sseReadTimeout is how long we wait for the next SSE line before
// treating the connection as stalled.
const sseReadTimeout = 5 * time.Minute

func (a *Anthropic) consumeSSE(body io.ReadCloser, ch chan<- provider.StreamEvent, model string) {
	defer close(ch)
	defer body.Close()

	log("sse: stream started, model=%s", model)

	// Wrap the body with a read deadline. We use a pipe + goroutine
	// because http.Response.Body doesn't support SetReadDeadline.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				pw.Write(buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					log("sse: body read error: %v", err)
				}
				return
			}
		}
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	var (
		msg       message.Message
		usage     message.Usage
		eventType string
		lines     int
	)
	msg.Role = message.RoleAssistant
	msg.Provider = providerName
	msg.Model = model
	msg.Timestamp = time.Now().UnixMilli()

	// Read with timeout: if no line arrives within sseReadTimeout,
	// we treat the stream as dead.
	scanCh := make(chan string, 1)
	scanErr := make(chan error, 1)
	scanNext := func() {
		go func() {
			if scanner.Scan() {
				scanCh <- scanner.Text()
			} else {
				scanErr <- scanner.Err()
			}
		}()
	}

	scanNext()
	for {
		var line string
		select {
		case line = <-scanCh:
			lines++
			scanNext() // start reading next line immediately
		case err := <-scanErr:
			if err != nil {
				log("sse: scanner error after %d lines: %v", lines, err)
			} else {
				log("sse: stream ended after %d lines (EOF)", lines)
			}
			goto done
		case <-time.After(sseReadTimeout):
			log("sse: read timeout after %d lines (%v with no data)", lines, sseReadTimeout)
			msg.StopReason = message.StopAborted
			ch <- provider.StreamEvent{Done: true, Message: &msg,
				Err: fmt.Errorf("SSE stream stalled: no data for %v", sseReadTimeout)}
			return
		}
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
				log("sse: message_start input_tokens=%d cache_read=%d cache_create=%d",
					usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			}

		case "message_stop":
			log("sse: message_stop output_tokens=%d stop_reason=%s", usage.OutputTokens, msg.StopReason)
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
			log("sse: received error event: %s", errEvt.Error.Message)
			msg.StopReason = message.StopError
			ch <- provider.StreamEvent{Done: true, Message: &msg,
				Err: fmt.Errorf("anthropic stream error: %s", errEvt.Error.Message)}
			return
		}
	}

done:
	if msg.StopReason == "" {
		log("sse: stream ended without message_stop after %d lines", lines)
		msg.StopReason = message.StopAborted
		ch <- provider.StreamEvent{Done: true, Message: &msg, Err: fmt.Errorf("stream ended unexpectedly")}
	}
}
