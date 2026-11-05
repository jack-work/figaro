package rpc

import "encoding/json"

type FormView struct {
	List         string       `json:"list"`
	Rows         string       `json:"rows,omitempty"`
	GroupColumns []ViewColumn `json:"group_columns,omitempty"`
	Columns      []ViewColumn `json:"columns,omitempty"`
	Sort         string       `json:"sort,omitempty"`
	Skip         string       `json:"skip,omitempty"`
	Mark         *ViewMark    `json:"mark,omitempty"`
	Actions      []ViewAction `json:"actions,omitempty"`
}

type ViewFormat string

const (
	FormatValue ViewFormat = ""
	FormatCount ViewFormat = "count"
	FormatTime  ViewFormat = "time"
	FormatAge   ViewFormat = "age"
)

type ViewColumn struct {
	Key    string     `json:"key"`
	Title  string     `json:"title,omitempty"`
	Right  bool       `json:"right,omitempty"`
	Shrink int        `json:"shrink,omitempty"`
	Format ViewFormat `json:"format,omitempty"`
	Alert  bool       `json:"alert,omitempty"`
}

type ViewMark struct {
	Key          string `json:"key,omitempty"`
	Equals       string `json:"equals,omitempty"`
	GroupSubject string `json:"group_subject,omitempty"`
	RowSubject   string `json:"row_subject,omitempty"`
}

type ViewTarget string

const (
	TargetRow     ViewTarget = ""
	TargetSubject ViewTarget = "subject"
)

type ViewArg string

const (
	ArgGroup ViewArg = "group"
	ArgRow   ViewArg = "row"
)

type ViewAction struct {
	Key     string             `json:"key"`
	Label   string             `json:"label"`
	Method  string             `json:"method"`
	Params  json.RawMessage    `json:"params,omitempty"`
	Confirm bool               `json:"confirm,omitempty"`
	Target  ViewTarget         `json:"target,omitempty"`
	Args    map[string]ViewArg `json:"args,omitempty"`
}
