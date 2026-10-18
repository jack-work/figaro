package decide

import (
	"encoding/json"
	"fmt"
	"strings"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/rpc"
)

const (
	systemOneLimit = 32768
	openAILimit    = 1000000
)

type dialect interface {
	api() string
	path() string
	limit() int
	question(p plan) any
	body(model string, s state, p plan) (any, int, error)
	decode(raw []byte) (response, error)
}

func dialectFor(api string) (dialect, error) {
	switch api {
	case apidecide.APISystemOne:
		return systemOne{}, nil
	case apidecide.APIOpenAI:
		return openAI{}, nil
	}
	return nil, fmt.Errorf("no decision API %q", api)
}

func askOf(p plan) string {
	if p.ask == "" && p.kind != rpc.DecideYesNo {
		return choiceDefault
	}
	return p.ask
}

type systemOne struct{}

func (systemOne) api() string  { return apidecide.APISystemOne }
func (systemOne) path() string { return "/systemone" }
func (systemOne) limit() int   { return systemOneLimit }

func (systemOne) question(p plan) any {
	switch p.kind {
	case rpc.DecideYesNo:
		return noulQuestion(askOf(p))
	case rpc.DecideScore:
		return scoreQuestion(askOf(p), p.options)
	}
	return choiceQuestion(askOf(p), p.options)
}

func (d systemOne) body(model string, s state, p plan) (any, int, error) {
	raw, err := encodeState(s, p.ask)
	if err != nil {
		return nil, 0, err
	}
	q, _ := d.question(p).(question)
	return request{Model: model, State: raw, Questions: map[string]question{questionKey: q}}, tokensOf(string(raw)), nil
}

func (systemOne) decode(raw []byte) (response, error) {
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%s: decode response: %w", Name, err)
	}
	return out, nil
}

type openAI struct{}

type openAIRequest struct {
	Model     string           `json:"model"`
	Input     string           `json:"input"`
	Questions []openAIQuestion `json:"questions"`
}

type openAIQuestion struct {
	Type         string         `json:"type"`
	Name         string         `json:"name"`
	Instructions string         `json:"instructions"`
	Choices      []openAIChoice `json:"choices,omitempty"`
}

type openAIChoice struct {
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type openAIResponse struct {
	Model   string `json:"model"`
	Answers []struct {
		Type          string   `json:"type"`
		Name          string   `json:"name"`
		Probability   *float64 `json:"probability"`
		Choice        string   `json:"choice"`
		Confidence    *float64 `json:"confidence"`
		Probabilities []struct {
			Value       string  `json:"value"`
			Probability float64 `json:"probability"`
		} `json:"probabilities"`
	} `json:"answers"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (openAI) api() string  { return apidecide.APIOpenAI }
func (openAI) path() string { return "/decisions" }
func (openAI) limit() int   { return openAILimit }

func (openAI) question(p plan) any {
	q := openAIQuestion{Type: "predicate", Name: questionKey, Instructions: askOf(p)}
	if p.kind != rpc.DecideYesNo {
		q.Type = "choice"
		for _, o := range p.options {
			desc := o.criterion
			if desc == o.label {
				desc = ""
			}
			q.Choices = append(q.Choices, openAIChoice{Value: o.label, Description: desc})
		}
	}
	return q
}

func (d openAI) body(model string, s state, p plan) (any, int, error) {
	input := inputText(s, p.ask)
	q, _ := d.question(p).(openAIQuestion)
	return openAIRequest{Model: model, Input: input, Questions: []openAIQuestion{q}}, estimate(input), nil
}

func (openAI) decode(raw []byte) (response, error) {
	var in openAIResponse
	if err := json.Unmarshal(raw, &in); err != nil {
		return response{}, fmt.Errorf("%s: decode response: %w", Name, err)
	}
	out := response{Model: in.Model, Answers: map[string]answer{}, Usage: in.Usage}
	for _, a := range in.Answers {
		if a.Name != questionKey && len(in.Answers) > 1 {
			continue
		}
		switch a.Type {
		case "predicate":
			out.Answers[questionKey] = answer{Type: "noul", Noul: a.Probability, Confidence: a.Confidence}
		case "choice":
			probs := make(map[string]float64, len(a.Probabilities))
			for _, p := range a.Probabilities {
				probs[p.Value] = p.Probability
			}
			out.Answers[questionKey] = answer{Type: "choice", Choice: a.Choice, Confidence: a.Confidence, Probabilities: probs}
		case "refusal":
			return out, fmt.Errorf("%s: the model declined to answer", Name)
		default:
			return out, fmt.Errorf("%s: an answer of type %q, which figaro does not ask for", Name, a.Type)
		}
	}
	return out, nil
}

func inputText(s state, ask string) string {
	if s.empty() {
		return ask
	}
	var parts []string
	if s.Focus != "" {
		parts = append(parts, "Focus (the passage the question is about):\n"+s.Focus)
	}
	if s.Context != "" {
		parts = append(parts, "Context:\n"+s.Context)
	}
	if len(s.Conversation) > 0 {
		parts = append(parts, "Conversation, oldest first:\n"+strings.Join(s.Conversation, "\n\n"))
	}
	return strings.Join(parts, "\n\n")
}
