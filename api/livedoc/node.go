package livedoc

import (
	"encoding/json"
	"reflect"
)

// A live unit is an append-only list of Nodes that only grows at the tail
// and whose nodes mutate monotonically, never reorder: nodes are addressed by
// index and diffed positionally.

type NodeType string

const (
	NodeProse    NodeType = "prose"
	NodeThinking NodeType = "thinking" // Markdown field
	NodeTool     NodeType = "tool"
	NodeSteering NodeType = "steering" // a user message injected mid-turn; Markdown field
)

const (
	StatusRunning = "running"
	StatusOK      = "ok"
	StatusError   = "error"
)

// Src is one fig-IR coordinate a node was projected from; a tool node has
// two, the invoke and its result.
type Src struct {
	LT    uint64 `json:"lt"`
	Block int    `json:"block"`
}

// RoleInput and RoleOutput mirror message.RoleInput / message.RoleOutput.
const (
	RoleInput  = "input"
	RoleOutput = "output"
)

// FormKind is decided server-side: a form carrying target-aria is a role.
type FormKind string

const (
	FormBound   FormKind = "bound"   // the aria's own bound form
	FormStudied FormKind = "studied" // an observed unbound form
	FormRole    FormKind = "role"    // a studied form carrying target-aria
)

// FormEvent distinguishes a key removed from the whole form deleted.
type FormEvent string

const (
	FormSet     FormEvent = "set"
	FormRemoved FormEvent = "removed"
	FormDeleted FormEvent = "deleted" // the source died; the copy records it
)

type FormDelta struct {
	Value json.RawMessage `json:"value,omitempty"` // absent on removal
	Prev  json.RawMessage `json:"prev,omitempty"`  // absent when the key is new
	Kind  FormKind        `json:"kind"`
	Event FormEvent       `json:"event,omitempty"`
	Form  string          `json:"form"` // the id, unsplit
}

// Node: only the fields for its Type are meaningful. Markdown and Output are
// the splice-patchable fields.
type Node struct {
	Type NodeType `json:"type"`

	// Role is RoleInput only for steering interjections: the opening question
	// is Turn.Inquiry, not a node.
	Role string `json:"role,omitempty"`

	// LTs is fig-IR provenance, never an address; derived from Src.
	LTs []uint64 `json:"lts,omitempty"`

	Src []Src `json:"src,omitempty"`

	// ToolCallID is the provider's receipt, not the node's identity.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// Version bumps on every mutation while the message is open; meaningful
	// only in the live section.
	Version int `json:"v,omitempty"`

	// At is the unix-millis time of the fig IR message this node came from.
	At int64 `json:"at,omitempty"`

	Markdown string `json:"markdown,omitempty"`

	// Sender is an aria id or a caller's label; one message can fold several
	// submissions, so attribution lives on the node. Empty draws nothing.
	// SenderName is the daemon's unique handle for an aria sender.
	Sender     string `json:"sender,omitempty"`
	SenderName string `json:"sender_name,omitempty"`

	// ID is legacy provenance, serialized but not UI identity: the wire
	// addresses a node by the positional pair (turn, From+i).
	ID     string                 `json:"id,omitempty"`
	Name   string                 `json:"name,omitempty"`
	Args   map[string]interface{} `json:"args,omitempty"`
	Status string                 `json:"status,omitempty"` // running | ok | error
	// Input is the raw, possibly truncated JSON prefix of the arguments as
	// streamed; Args exists only once the whole object parses.
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	// OutputBase is the rune offset of Output within the source block, which
	// the producer truncates to its last lines.
	OutputBase int    `json:"output_base,omitempty"`
	Summary    string `json:"summary,omitempty"` // client renders verbatim
	// OpenedAt is when the tool block opened on the provider stream;
	// StartedAt is when the call began running.
	OpenedAt   int64 `json:"opened_at,omitempty"`
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`

	// FormDeltas is keyed "<formid>.<path>"; every node type tolerates it.
	FormDeltas map[string]FormDelta `json:"formDeltas,omitempty"`
}

type OpKind string

const (
	OpOpen  OpKind = "open"  // append a new node at Index
	OpPatch OpKind = "patch" // splice a string field of an existing node
	OpSet   OpKind = "set"   // update a tool node's scalar fields
)

type Op struct {
	Kind  OpKind `json:"kind"`
	Index int    `json:"index"`

	Node *Node `json:"node,omitempty"` // open

	Field string `json:"field,omitempty"` // patch: "markdown" | "output"
	At    int    `json:"at,omitempty"`
	Del   int    `json:"del,omitempty"`
	Ins   string `json:"ins,omitempty"`

	// set
	Status     string                 `json:"status,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Args       map[string]interface{} `json:"args,omitempty"`
	OpenedAt   int64                  `json:"opened_at,omitempty"`
	StartedAt  int64                  `json:"started_at,omitempty"`
	FinishedAt int64                  `json:"finished_at,omitempty"`
	OutputBase int                    `json:"output_base,omitempty"`
}

// DiffNodes compares index by index; nil when nothing changed.
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
			if o.Status != n.Status || o.Name != n.Name || !sameArgs(o.Args, n.Args) ||
				o.StartedAt != n.StartedAt || o.FinishedAt != n.FinishedAt ||
				o.OutputBase != n.OutputBase {
				ops = append(ops, Op{
					Kind:       OpSet,
					Index:      i,
					Status:     n.Status,
					Name:       n.Name,
					Args:       n.Args,
					StartedAt:  n.StartedAt,
					FinishedAt: n.FinishedAt,
					OutputBase: n.OutputBase,
				})
			}
		}
	}
	return ops
}

// ApplyOp ignores out-of-range indices; consumers resync via snapshot.
func ApplyOp(nodes []Node, op Op) []Node {
	switch op.Kind {
	case OpOpen:
		if op.Node == nil {
			return nodes
		}
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
		nodes[op.Index].OutputBase = op.OutputBase
	}
	return nodes
}

func sameArgs(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	return reflect.DeepEqual(a, b)
}
