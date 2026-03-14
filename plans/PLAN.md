# Figaro: Design Plan

> *Largo al factotum della città!*

![Architecture sketch](dell-monitor-screenshot.png)

## Overview

Figaro is a minimal, Go-native coding agent. Each agent instance is called
a "figaro" (after the barber of Seville). The runtime manages a table of
figaros, each with its own conversation context and store.

## Core Concepts

### IR (Intermediate Representation)

Messages use a provider-agnostic canonical format. Each message carries
a `Baggage` map keyed by provider name, holding the unaltered original
wire representation. This avoids NxM translations:

- Provider receives IR block → checks baggage for cache hit → converts
  from IR only on miss
- Provider streams response → wraps in IR → stashes native form in baggage
- Switch providers mid-conversation: new provider ignores old baggage,
  converts from IR, starts populating its own

### Logical Time

Each message gets a monotonic `uint64` counter called `logical_time`.
One per tic, uniquely identifies the message in the session. Used for
ordering, branching, and entry references. No UUIDs, no content hashes.

### message.Block

The unit of conversation context:

```go
type Block struct {
    Header   *Message    // compacted summary (nil if no compaction)
    Messages []Message   // ordered conversation, first-kept to leaf
}
```

`Store.Context()` returns `*Block`. `Provider.Send()` accepts `*Block`.
The provider converts the block to its native format internally.

## Interfaces

### Store

One interface, all layers. The agent never knows what's behind it.

```go
type Store interface {
    Context() *message.Block          // get conversation for LLM
    Append(msg message.Message) (uint64, error)  // add message, get logical time
    Branch(logicalTime uint64) error  // fork from earlier point
    LeafTime() uint64                 // current leaf logical time
    SessionID() string
    Close() error
}
```

Compaction is internal to the store. The agent loop never triggers it.

### Provider

The reverse of a store: receives context, returns messages.

```go
type Provider interface {
    Name() string
    Send(ctx, *message.Block, tools, maxTokens) (<-chan StreamEvent, error)
}
```

The provider's output channel streams `StreamEvent`s. Each event carries
the accumulated IR message. On `Done`, the message has baggage populated.

### Registry

Maps figaro IDs → Store instances.

```go
type Registry interface {
    Get(figaroID string) (Store, error)
    List() []string
    Close() error
}
```

## The Tic Loop

Synchronous state machine. Each tic processes one message:

```
append user message
  │
  ▼
┌─────────────────────────────────────┐
│ TIC:                                │
│   block := store.Context()          │
│   last := block.Messages[last]      │
│                                     │
│   user | tool_result →              │
│     provider.Send(block)            │
│     stream deltas to Out chan       │
│     store.Append(assistantMsg)      │
│     → next tic                     │
│                                     │
│   assistant + tool_calls →          │
│     execute each tool               │
│     store.Append(toolResultMsg)     │
│     → next tic                     │
│                                     │
│   assistant + stop →                │
│     emit done                       │
│     return                          │
└─────────────────────────────────────┘

Every Append also emits a JSON-RPC 2.0 notification to the Out channel.
```

### How pi handles this (for reference)

Pi's agent-loop.ts has the same bones but with async complexity:

1. Outer loop: runs until no tool calls AND no pending messages
2. Each turn: stream assistant → check tool calls
3. If tool calls: execute sequentially, check for steering messages
   between each (user typed while tools ran). If steering, skip remaining.
4. If no tool calls: check follow-up messages. If any, continue.
5. Stop on: `stop`, `error`, `aborted`

Figaro step 1 simplifies: no steering, no follow-ups, single operator,
one message at a time. Equivalent to pi's inner loop stripped down.

## JSON-RPC 2.0 Output

The process writes newline-delimited notifications to stdout:

```jsonl
{"jsonrpc":"2.0","method":"stream.delta","params":{"text":"I'll fix","content_type":"text"}}
{"jsonrpc":"2.0","method":"stream.tool_start","params":{"tool_name":"bash","arguments":{"command":"ls"}}}
{"jsonrpc":"2.0","method":"stream.tool_end","params":{"tool_name":"bash","result":"main.go\ngo.mod"}}
{"jsonrpc":"2.0","method":"stream.message","params":{"logical_time":3,"message":{...}}}
{"jsonrpc":"2.0","method":"stream.done","params":{"session_id":"abc","reason":"stop"}}
```

The `stream.message` notification carries the full IR message including
baggage. The stdout stream is a live view of what the store persists.

---

## Roadmap

### Step 1: Core Agent Loop (NOW)

```
$ figaro --session abc123 "fix the bug"
```

Process starts, opens JSONL store, runs tic loop, streams to stdout, exits.

**Deliverables**:
- [x] `message.Message` IR with baggage and logical time
- [x] `message.Block` as context unit
- [x] `store.Store` interface
- [x] `store.Registry` interface
- [x] `provider.Provider` interface (Send accepts Block)
- [x] `provider/anthropic` — raw HTTP+SSE, baggage populated
- [x] JSON-RPC 2.0 notification types
- [x] Tic-based agent loop
- [ ] JSONL store implementation (implements Store)
- [ ] CLI entrypoint wiring (registry, store, tools, system prompt)
- [ ] OAuth token support (Claude Max — read from ~/.pi/agent/auth.json or own auth)

### Step 2: In-Memory WAL

```
agent ──► MemoryStore ──► JSONLStore ──► disk
```

Same interface. MemoryStore decorates JSONLStore:
- `Context()` → from memory (fast)
- `Append()` → to memory immediately, flush to JSONL periodically
- Startup: seed from inner `Context()` if cold
- Compaction: internal, when WAL hits size threshold

### Step 3: Daemon + Frontend

- Long-running daemon holds the Registry and warm stores
- CLI becomes thin client (unix socket)
- Frontend reads JSON-RPC, renders with goldmark/bubbletea
- Warm HTTP/2 to Anthropic

### Step X: Archive Rotation

When WAL is full:
1. mv existing JSONL to archive (stamped)
2. Compact WAL contents
3. Reset WAL with compacted header
4. New JSONL starts fresh with just the summary

Archive accumulates — full history preserved, not loaded by default.
