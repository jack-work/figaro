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

type Cursor struct {
	Turn    uint64 `json:"turn"`
	Text    int    `json:"text"`
	Started int    `json:"started"`
	Ended   int    `json:"ended"`
}

type Event struct {
	Kind   Kind   `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   *Tool  `json:"tool,omitempty"`
	Cursor Cursor `json:"cursor"`
}

const MaxToolText = 4 << 10
