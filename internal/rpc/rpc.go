// Package rpc defines the JSON-RPC 2.0 types shared across figaro components.
//
// Two protocols use these types:
//
//  1. Figaro socket (agent ops): prompt, context, subscribe, info
//     + stream notifications (delta, message, tool_start, etc.)
//
//  2. Angelus socket (registry ops): create, kill, list, info, bind, resolve, unbind, status
//
// All communication across process boundaries is JSON-RPC 2.0.
// This package defines the shared types so that any client in any
// language can implement the protocol.
package rpc

import "github.com/jack-work/figaro/internal/message"

// --- Figaro socket: notification params (streamed to subscribers) ---

// Notification is a JSON-RPC 2.0 notification (no id, no response).
// Used internally by the agent to emit events. When sent over jrpc2,
// the library handles framing — this type is for the in-process channel.
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type DeltaParams struct {
	Text        string              `json:"text"`
	ContentType message.ContentType `json:"content_type"`
}

type ThinkingParams struct {
	Text string `json:"text"`
}

type ToolStartParams struct {
	ToolCallID string                 `json:"tool_call_id"`
	ToolName   string                 `json:"tool_name"`
	Arguments  map[string]interface{} `json:"arguments,omitempty"`
}

type ToolEndParams struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Result     string `json:"result"`
	IsError    bool   `json:"is_error,omitempty"`
}

type MessageParams struct {
	LogicalTime uint64          `json:"logical_time"`
	Message     message.Message `json:"message"`
}

type DoneParams struct {
	Reason string `json:"reason"`
}

type ErrorParams struct {
	Message string `json:"message"`
}
