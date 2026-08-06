package livedoc

import "reflect"

// A live unit is an ordered, append-only list of typed Nodes. Prose and
// tool calls are distinct node types so a consumer can render a tool as a
// native widget instead of baked-in markdown. Within a unit the list only
// grows at the tail and existing nodes only mutate monotonically (prose
// text grows, tool output grows, tool status flips) — never reorder — so
// nodes are addressed by a stable index and diffed positionally.

// NodeType discriminates the node payload.
type NodeType string

const (
	NodeProse    NodeType = "prose"    // a markdown span (assistant text)
	NodeThinking NodeType = "thinking" // extended-thinking text (Markdown field)
	NodeTool     NodeType = "tool"     // a tool invocation + its streamed result
	NodeSteering NodeType = "steering" // a user message injected mid-turn (Markdown field)
)

// Tool status values.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusError   = "error"
)

// Src is one fig-IR coordinate a node was projected from: the owning
// message's logical time and the content block's index within it. A node
// usually has one; a tool node has two — the invoke and its result — at
// different block indices in different messages.
type Src struct {
	LT    uint64 `json:"lt"`
	Block int    `json:"block"`
}

// RoleInput and RoleOutput are the two voices of the UI IR, mirroring
// message.RoleInput / message.RoleOutput. They live here because Node.Role
// lives here, and they are constants rather than literals because the same two
// strings were previously spelled out at twenty call sites across four
// packages — the shape that lets a vocabulary drift.
const (
	RoleInput  = "input"
	RoleOutput = "output"
)

// Node is one element of a live unit. Only the fields for its Type are
// meaningful; the rest are zero. The two long, streamed string fields —
// prose Markdown and tool Output — are the splice-patchable ones.
type Node struct {
	Type NodeType `json:"type"`

	// Role is RoleInput for steering interjections, RoleOutput otherwise. The
	// turn's opening question is NOT a node — it is Turn.Inquiry, plain text —
	// so a node carrying RoleInput is always a steering interjection.
	Role string `json:"role,omitempty"`

	// LTs is fig-IR provenance — metadata, never an address. UI code addresses
	// by (turn, node); LT is the model's coordinate. Derived from Src.
	LTs []uint64 `json:"lts,omitempty"`

	// Src is the precise per-source coordinate behind LTs.
	Src []Src `json:"src,omitempty"`

	// ToolCallID is the provider's handle for a tool node. It is a receipt,
	// not an identity: ID answers "which node", ToolCallID answers "which
	// provider call". Keeping them apart is the point of this split.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// Version bumps on every mutation of this node while its message is open,
	// so a UI element bound to (ID, Version) knows when to repaint and a client
	// can detect a missed update. Meaningful only in the live section.
	Version int `json:"v,omitempty"`

	// At is the wall-clock time (unix millis) of the fig IR message this node
	// came from. Carried so the UI can show when a node was written, the way it
	// already shows tool timings; surfaced under the verbose toggle.
	At int64 `json:"at,omitempty"`

	// prose
	Markdown string `json:"markdown,omitempty"`

	// Sender attributes an input block to whoever submitted it, already
	// rendered (see rpc.Attribution). One user message can fold several
	// submissions from different callers, so attribution belongs on the NODE,
	// not on the message. Empty means unknown and draws nothing at all.
	Sender string `json:"sender,omitempty"`

	// ID is legacy provenance/provider metadata: prose/thinking use an LT.block
	// receipt and tools use the provider's tool-call id. It remains serialized
	// in snapshots, but it is not UI identity. The aria wire addresses a node by
	// the positional pair (turn, From+i), and NodeDelta.ID is that uint64 ordinal.
	ID     string                 `json:"id,omitempty"`
	Name   string                 `json:"name,omitempty"`   // tool name
	Args   map[string]interface{} `json:"args,omitempty"`   // invocation arguments
	Status string                 `json:"status,omitempty"` // running | ok | error
	// Input is the tool's arguments AS THEY ARRIVE — the raw, still-truncated
	// JSON prefix the provider is streaming. Args is the same thing decoded,
	// and only exists once the whole object parses, so Input is what there is
	// to show while the model is still writing it.
	//
	// It is a STREAMED field (spliced by livedoc.Diff, like markdown and
	// output), and it is not append-only: a bounded tail drops leading bytes
	// as it slides, and it is cleared when Args lands. Both are ordinary
	// deltas — Delta carries Del as well as Ins — so a shrink costs one splice
	// and needs nothing new on the wire.
	Input   string `json:"input,omitempty"`
	Output  string `json:"output,omitempty"`  // streamed result text
	Summary string `json:"summary,omitempty"` // producer-computed one-line tool description (client renders verbatim)
	// OpenedAt is when the model began WRITING this call — the moment the tool
	// block opened on the provider stream, before a single argument byte had
	// arrived. StartedAt is when the call began RUNNING. The gap between them
	// is GENERATION, which for a large write is nearly all of the wall time
	// and used to be invisible: a thirty-second write rendered [0ms], because
	// the only clock there started after the writing was over.
	OpenedAt   int64 `json:"opened_at,omitempty"`
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
}

// OpKind discriminates a node mutation on the wire.
type OpKind string

const (
	OpOpen  OpKind = "open"  // append a new node at Index
	OpPatch OpKind = "patch" // splice a string field of an existing node
	OpSet   OpKind = "set"   // update a tool node's scalar fields (status,
	// name, args) — e.g. when the streamed tool_use arguments arrive after
	// the block first opened
)

// Op is one mutation against a unit's node list, addressed by Index.
// Open carries the full new node; Patch carries a field splice; Set
// carries new scalar tool state.
type Op struct {
	Kind  OpKind `json:"kind"`
	Index int    `json:"index"`

	Node *Node `json:"node,omitempty"` // open

	Field string `json:"field,omitempty"` // patch: "markdown" | "output"
	At    int    `json:"at,omitempty"`
	Del   int    `json:"del,omitempty"`
	Ins   string `json:"ins,omitempty"`

	// set: a tool node's scalar fields.
	Status string                 `json:"status,omitempty"`
	Name   string                 `json:"name,omitempty"`
	Args   map[string]interface{} `json:"args,omitempty"`
	// OpenedAt is when the model began WRITING this call — the moment the
	// tool block opened on the provider stream, before a single argument
	// byte had arrived. StartedAt is when the call began RUNNING. The gap
	// between them is generation, which for a large write is nearly all of
	// the wall time and used to be invisible: a thirty-second write read
	// [0ms], because the only clock started after the writing was over.
	OpenedAt   int64 `json:"opened_at,omitempty"`
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
}

// DiffNodes derives the minimal op sequence turning old into next. The
// list is append-only and positionally stable, so it compares index by
// index: appended nodes become Open ops; a prose/output text change
// becomes a Patch (single-region splice); a tool status change becomes a
// Set. Returns nil when nothing changed.
func DiffNodes(old, next []Node) []Op {
	var ops []Op
	for i := 0; i < len(next); i++ {
		if i >= len(old) {
			n := next[i]
			ops = append(ops, Op{Kind: OpOpen, Index: i, Node: &n})
			continue
		}
		o, n := old[i], next[i]
		switch n.Type {
		case NodeProse, NodeThinking:
			if d, ok := Diff(o.Markdown, n.Markdown); ok {
				ops = append(ops, Op{Kind: OpPatch, Index: i, Field: "markdown", At: d.At, Del: d.Del, Ins: d.Ins})
			}
		case NodeTool:
			if d, ok := Diff(o.Output, n.Output); ok {
				ops = append(ops, Op{Kind: OpPatch, Index: i, Field: "output", At: d.At, Del: d.Del, Ins: d.Ins})
			}
			// Tool args/name stream in after the block opens, so a Set
			// carries them (and status) whenever any scalar field changes.
			if o.Status != n.Status || o.Name != n.Name || !sameArgs(o.Args, n.Args) ||
				o.StartedAt != n.StartedAt || o.FinishedAt != n.FinishedAt {
				ops = append(ops, Op{
					Kind:       OpSet,
					Index:      i,
					Status:     n.Status,
					Name:       n.Name,
					Args:       n.Args,
					StartedAt:  n.StartedAt,
					FinishedAt: n.FinishedAt,
				})
			}
		}
	}
	return ops
}

// ApplyOp folds one op into a node list, returning the updated slice.
// Out-of-range indices are clamped/ignored so a malformed op degrades to
// a near-no-op rather than panicking (consumers resync via snapshot).
func ApplyOp(nodes []Node, op Op) []Node {
	switch op.Kind {
	case OpOpen:
		if op.Node == nil {
			return nodes
		}
		// Append (Index is advisory; the list is tail-only).
		return append(nodes, *op.Node)
	case OpPatch:
		if op.Index < 0 || op.Index >= len(nodes) {
			return nodes
		}
		d := Delta{At: op.At, Del: op.Del, Ins: op.Ins}
		if op.Field == "output" {
			nodes[op.Index].Output = Apply(nodes[op.Index].Output, d)
		} else {
			nodes[op.Index].Markdown = Apply(nodes[op.Index].Markdown, d)
		}
	case OpSet:
		if op.Index < 0 || op.Index >= len(nodes) {
			return nodes
		}
		nodes[op.Index].Status = op.Status
		if op.Name != "" {
			nodes[op.Index].Name = op.Name
		}
		if op.Args != nil {
			nodes[op.Index].Args = op.Args
		}
		if op.StartedAt != 0 {
			nodes[op.Index].StartedAt = op.StartedAt
		}
		if op.FinishedAt != 0 {
			nodes[op.Index].FinishedAt = op.FinishedAt
		}
	}
	return nodes
}

// sameArgs reports whether two tool-argument maps are equal.
func sameArgs(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	return reflect.DeepEqual(a, b)
}
