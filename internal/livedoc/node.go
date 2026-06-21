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
)

// Tool status values.
const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusError   = "error"
)

// Node is one element of a live unit. Only the fields for its Type are
// meaningful; the rest are zero. The two long, streamed string fields —
// prose Markdown and tool Output — are the splice-patchable ones.
type Node struct {
	Type NodeType `json:"type"`

	// prose
	Markdown string `json:"markdown,omitempty"`

	// tool
	ID     string                 `json:"id,omitempty"`     // tool_call_id (stable handle)
	Name   string                 `json:"name,omitempty"`   // tool name
	Args   map[string]interface{} `json:"args,omitempty"`   // invocation arguments
	Status string                 `json:"status,omitempty"` // running | ok | error
	Output string                 `json:"output,omitempty"` // streamed result text
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
			if o.Status != n.Status || o.Name != n.Name || !sameArgs(o.Args, n.Args) {
				ops = append(ops, Op{Kind: OpSet, Index: i, Status: n.Status, Name: n.Name, Args: n.Args})
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
