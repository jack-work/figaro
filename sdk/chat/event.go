// Package chat streams a figaro aria's replies as chat events: text deltas,
// tool calls and turn ends, resumable from a cursor. docs/figchat-stream.md
// is the contract.
package chat

type Kind string

const (
	Delta     Kind = "delta"
	ToolStart Kind = "tool.start"
	ToolEnd   Kind = "tool.end"
	End       Kind = "end"
	Failed    Kind = "error"
)

type Tool struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Cursor is how much of a turn a consumer has been given. Text counts bytes
// of the turn's reply text; Started and Ended count tool events, which are
// released in the order their tools appear in the turn.
type Cursor struct {
	Turn    uint64 `json:"turn"`
	Text    int    `json:"text"`
	Started int    `json:"started"`
	Ended   int    `json:"ended"`
}

// Event.Cursor is the position after the event; Resume from it continues
// with the next one.
type Event struct {
	Kind   Kind   `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   *Tool  `json:"tool,omitempty"`
	Cursor Cursor `json:"cursor"`
}

const MaxToolText = 4 << 10
