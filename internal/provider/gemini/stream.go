package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

const maxSSEEventBytes = 32 << 20

var callSeq atomic.Uint64

type candidate struct {
	Content struct {
		Role  string `json:"role"`
		Parts []Part `json:"parts"`
	} `json:"content"`
	FinishReason string `json:"finishReason"`
}

type usageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}

type generatePayload struct {
	Candidates    []candidate   `json:"candidates"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
	ModelVersion  string        `json:"modelVersion"`
}

type streamChunk struct {
	Candidates    []candidate      `json:"candidates"`
	UsageMetadata usageMetadata    `json:"usageMetadata"`
	ModelVersion  string           `json:"modelVersion"`
	Response      *generatePayload `json:"response"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

type assembled struct {
	Blocks     []message.Content
	WireFinish string
	HasCalls   bool
	Usage      *message.Usage
}

func drainSSE(ctx context.Context, body io.Reader, bus provider.Bus) (assembled, error) {
	var (
		out       assembled
		lastUsage *usageMetadata
		events    int
		sawFinish bool
		done      bool
		eventBuf  bytes.Buffer
		hasData   bool
	)

	handleEvent := func(payload string) error {
		payload = strings.TrimSpace(payload)
		if payload == "" {
			return nil
		}
		if payload == "[DONE]" {
			done = true
			return nil
		}
		events++
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil
		}
		if chunk.Error != nil {
			return fmt.Errorf("upstream %s (%d): %s", chunk.Error.Status, chunk.Error.Code, redactErrorText(chunk.Error.Message))
		}

		inner := generatePayload{
			Candidates:    chunk.Candidates,
			UsageMetadata: chunk.UsageMetadata,
			ModelVersion:  chunk.ModelVersion,
		}
		if chunk.Response != nil {
			inner = *chunk.Response
		}

		um := inner.UsageMetadata
		if um.TotalTokenCount > 0 || um.PromptTokenCount > 0 || um.CandidatesTokenCount > 0 || um.ThoughtsTokenCount > 0 {
			cp := um
			lastUsage = &cp
		}

		if len(inner.Candidates) == 0 {
			return nil
		}
		cand := inner.Candidates[0]
		for _, p := range cand.Content.Parts {
			if p.FunctionCall != nil {
				out.HasCalls = true
				callID := p.FunctionCall.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), callSeq.Add(1))
				}
				args := p.FunctionCall.Args
				if args == nil {
					args = map[string]any{}
				}
				bus.PushToolInvokeStart(callID, p.FunctionCall.Name)
				if argBytes, err := json.Marshal(args); err == nil {
					bus.PushToolInvokeDelta(callID, string(argBytes))
				}
				block := message.Content{
					Type:       message.ContentToolInvoke,
					ToolCallID: callID,
					ToolName:   p.FunctionCall.Name,
					Arguments:  args,
					Signature:  p.ThoughtSignature,
				}
				out.Blocks = append(out.Blocks, block)
				bus.PushToolReady(block)
				continue
			}
			if p.Thought {
				if p.Text != "" || p.ThoughtSignature != "" {
					if n := len(out.Blocks); n > 0 && out.Blocks[n-1].Type == message.ContentThinking {
						b := &out.Blocks[n-1]
						b.Text += p.Text
						if p.ThoughtSignature != "" {
							b.Signature = p.ThoughtSignature
						}
					} else {
						out.Blocks = append(out.Blocks, message.Content{
							Type:      message.ContentThinking,
							Text:      p.Text,
							Signature: p.ThoughtSignature,
						})
					}
					bus.PushDelta(message.Content{
						Type:      message.ContentThinking,
						Text:      p.Text,
						Signature: p.ThoughtSignature,
					})
				}
				continue
			}
			if p.Text != "" || p.ThoughtSignature != "" {
				if n := len(out.Blocks); n > 0 && out.Blocks[n-1].Type == message.ContentProse {
					b := &out.Blocks[n-1]
					b.Text += p.Text
					if p.ThoughtSignature != "" {
						b.Signature = p.ThoughtSignature
					}
				} else {
					out.Blocks = append(out.Blocks, message.Content{
						Type:      message.ContentProse,
						Text:      p.Text,
						Signature: p.ThoughtSignature,
					})
				}
				if p.Text != "" {
					bus.PushDelta(message.Content{
						Type:      message.ContentProse,
						Text:      p.Text,
						Signature: p.ThoughtSignature,
					})
				}
			}
		}
		if cand.FinishReason != "" {
			sawFinish = true
			out.WireFinish = cand.FinishReason
		}
		return nil
	}

	flushEvent := func() error {
		if !hasData {
			return nil
		}
		payload := eventBuf.String()
		eventBuf.Reset()
		hasData = false
		return handleEvent(payload)
	}

	reader := bufio.NewReaderSize(body, 64*1024)
	for !done {
		select {
		case <-ctx.Done():
			out.Usage = usageToIR(lastUsage)
			return out, ctx.Err()
		default:
		}
		rawLine, readErr := reader.ReadBytes('\n')
		if len(rawLine) > 0 {
			provider.SawByte(ctx)
			line := strings.TrimRight(string(rawLine), "\r\n")
			if line == "" {
				if err := flushEvent(); err != nil {
					out.Usage = usageToIR(lastUsage)
					return out, err
				}
			} else if after, ok := strings.CutPrefix(line, "data:"); ok {
				part := strings.TrimPrefix(after, " ")
				if eventBuf.Len()+len(part)+1 > maxSSEEventBytes {
					out.Usage = usageToIR(lastUsage)
					return out, fmt.Errorf("gemini sse event exceeds %d bytes", maxSSEEventBytes)
				}
				if hasData {
					eventBuf.WriteByte('\n')
				}
				eventBuf.WriteString(part)
				hasData = true
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if err := flushEvent(); err != nil {
					out.Usage = usageToIR(lastUsage)
					return out, err
				}
				break
			}
			out.Usage = usageToIR(lastUsage)
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, &provider.StreamCut{Awaited: "finishReason", Events: events, Err: readErr}
		}
	}

	out.Usage = usageToIR(lastUsage)

	if !sawFinish && !done {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, &provider.StreamCut{Awaited: "finishReason", Events: events}
	}
	return out, nil
}

func usageToIR(u *usageMetadata) *message.Usage {
	if u == nil {
		return nil
	}
	if u.PromptTokenCount == 0 && u.CandidatesTokenCount == 0 && u.ThoughtsTokenCount == 0 && u.TotalTokenCount == 0 {
		return nil
	}
	input := u.PromptTokenCount - u.CachedContentTokenCount
	if input < 0 {
		input = 0
	}
	return &message.Usage{
		InputTokens:     input,
		OutputTokens:    u.CandidatesTokenCount + u.ThoughtsTokenCount,
		CacheReadTokens: u.CachedContentTokenCount,
	}
}

func (a assembled) toIRMessage() message.Message {
	msg := message.Message{
		Role:       message.RoleOutput,
		Usage:      a.Usage,
		StopReason: mapStopReason(a.WireFinish, a.HasCalls),
	}
	for _, b := range a.Blocks {
		switch b.Type {
		case message.ContentThinking:
			if b.Text != "" || b.Signature != "" {
				msg.Content = append(msg.Content, b)
			}
		case message.ContentProse:
			if b.Text != "" || b.Signature != "" {
				msg.Content = append(msg.Content, b)
			}
		case message.ContentToolInvoke:
			if b.ToolCallID != "" && b.ToolName != "" {
				msg.Content = append(msg.Content, b)
			}
		}
	}
	return msg
}

func mapStopReason(wireFinish string, hasCalls bool) message.StopReason {
	if hasCalls {
		return message.StopToolInvoke
	}
	switch strings.ToUpper(strings.TrimSpace(wireFinish)) {
	case "MAX_TOKENS":
		return message.StopLength
	case "STOP", "":
		return message.StopEnd
	default:
		return message.StopError
	}
}
