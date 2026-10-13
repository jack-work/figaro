package decide

import (
	"encoding/json"
	"strings"
)

type request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]question `json:"questions"`
}

type question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Noul          *float64           `json:"noul"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type state struct {
	Focus        string   `json:"focus,omitempty"`
	Context      string   `json:"context,omitempty"`
	Conversation []string `json:"conversation,omitempty"`
}

func (s state) empty() bool {
	return s.Focus == "" && s.Context == "" && len(s.Conversation) == 0
}

func choiceQuestion(ask string, opts []option) question {
	criteria := make(map[string]string, len(opts))
	for _, o := range opts {
		criteria[o.label] = o.criterion
	}
	return question{Type: "choice", Instructions: ask, Criteria: criteria}
}

func noulQuestion(ask string) question {
	return question{Type: "noul", Instructions: ask}
}

func estimate(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return tokensOf(string(b))
}

func tokensOf(s string) int {
	if s == "" {
		return 0
	}
	return (len(s)*bytesPerTokenDen + bytesPerTokenNum - 1) / bytesPerTokenNum
}

const (
	bytesPerTokenNum = 11
	bytesPerTokenDen = 4
)

func clip(s string, maxRunes int) string {
	const ellipsis = "…"
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	keep := maxRunes - 1
	if keep < 2 {
		return ellipsis
	}
	head := keep * 2 / 3
	return string(runes[:head]) + ellipsis + string(runes[len(runes)-(keep-head):])
}

func trimmed(s string) string { return strings.TrimSpace(s) }
