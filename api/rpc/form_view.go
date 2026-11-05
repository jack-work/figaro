package rpc

import (
	"encoding/json"
	"strings"
)

type FormView struct {
	Rows    []string     `json:"rows"`
	Open    []string     `json:"open,omitempty"`
	Sort    string       `json:"sort,omitempty"`
	Label   string       `json:"label,omitempty"`
	Skip    string       `json:"skip,omitempty"`
	Mark    *ViewMark    `json:"mark,omitempty"`
	Actions []ViewAction `json:"actions,omitempty"`
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

func MatchPath(patterns []string, segs []string) (group string, ok bool) {
	for _, p := range patterns {
		pat := strings.Split(p, ".")
		if g, ok := matchSegs(pat, segs, ""); ok {
			if strings.Count(p, "*") < 2 || pat[len(pat)-1] != "*" || strings.Contains(p, "**") {
				g = ""
			}
			return g, true
		}
	}
	return "", false
}

func matchSegs(pat, segs []string, group string) (string, bool) {
	if len(pat) == 0 {
		return group, len(segs) == 0
	}
	switch pat[0] {
	case "**":
		for n := 1; n <= len(segs); n++ {
			if g, ok := matchSegs(pat[1:], segs[n:], group); ok {
				return g, true
			}
		}
		return "", false
	case "*":
		if len(segs) == 0 {
			return "", false
		}
		if group == "" {
			group = segs[0]
		}
		return matchSegs(pat[1:], segs[1:], group)
	}
	if len(segs) == 0 || segs[0] != pat[0] {
		return "", false
	}
	return matchSegs(pat[1:], segs[1:], group)
}
