package rpc

import "encoding/json"

type FormView struct {
	List    string       `json:"list"`
	Columns []ViewColumn `json:"columns,omitempty"`
	Sort    string       `json:"sort,omitempty"`
	Mark    *ViewMark    `json:"mark,omitempty"`
	Actions []ViewAction `json:"actions,omitempty"`
}

type ViewFormat string

const (
	FormatValue ViewFormat = ""
	FormatCount ViewFormat = "count"
	FormatTime  ViewFormat = "time"
)

type ViewColumn struct {
	Key    string     `json:"key"`
	Title  string     `json:"title,omitempty"`
	Right  bool       `json:"right,omitempty"`
	Shrink int        `json:"shrink,omitempty"`
	Format ViewFormat `json:"format,omitempty"`
}
type ViewMark struct {
	Key    string `json:"key"`
	Equals string `json:"equals"`
}
type ViewAction struct {
	Key     string          `json:"key"`
	Label   string          `json:"label"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Confirm bool            `json:"confirm,omitempty"`
}
