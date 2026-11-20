package chat

import (
	"fmt"
	"strconv"
	"strings"
)

type Kind string

const (
	Delta     Kind = "delta"
	ToolStart Kind = "tool.start"
	ToolEnd   Kind = "tool.end"
	End       Kind = "end"
	Failed    Kind = "error"
	TurnStart Kind = "turn.start"
)

type Sender struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

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
	Begun   bool   `json:"begun,omitempty"`
}

func (c Cursor) String() string {
	s := fmt.Sprintf("%d.%d.%d.%d", c.Turn, c.Text, c.Started, c.Ended)
	if c.Begun {
		s += ".1"
	}
	return s
}

func ParseCursor(s string) (Cursor, error) {
	fields := strings.Split(s, ".")
	if len(fields) != 4 && len(fields) != 5 {
		return Cursor{}, fmt.Errorf("chat: cursor %q: want turn.text.started.ended", s)
	}
	var n [4]int64
	for i := range n {
		v, err := strconv.ParseInt(fields[i], 10, 64)
		if err != nil || v < 0 {
			return Cursor{}, fmt.Errorf("chat: cursor %q: bad number %q", s, fields[i])
		}
		n[i] = v
	}
	c := Cursor{Turn: uint64(n[0]), Text: int(n[1]), Started: int(n[2]), Ended: int(n[3])}
	if len(fields) == 5 {
		if fields[4] != "1" {
			return Cursor{}, fmt.Errorf("chat: cursor %q: bad flag %q", s, fields[4])
		}
		c.Begun = true
	}
	return c, nil
}

type Event struct {
	Kind   Kind    `json:"kind"`
	Text   string  `json:"text,omitempty"`
	Tool   *Tool   `json:"tool,omitempty"`
	Sender *Sender `json:"sender,omitempty"`
	Cursor Cursor  `json:"cursor"`
}

const MaxToolText = 4 << 10
