package rpc

type DecideRequest struct {
	FigaroID string   `json:"figaro_id,omitempty"`
	Question string   `json:"question"`
	Choices  []string `json:"choices,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Model    string   `json:"model,omitempty"`
	API      string   `json:"api,omitempty"`
	Context  string   `json:"context,omitempty"`
	Budget   int      `json:"budget,omitempty"`
}

type DecideKind string

const (
	DecideYesNo  DecideKind = "yes_no"
	DecideChoice DecideKind = "choice"
)

type DecideRow struct {
	Option      string  `json:"option"`
	Probability float64 `json:"probability"`
	Chosen      bool    `json:"chosen,omitempty"`
}

type DecideWindow struct {
	Messages     int      `json:"messages"`
	Tokens       int      `json:"tokens"`
	Budget       int      `json:"budget"`
	Focus        string   `json:"focus,omitempty"`
	FormRefs     []string `json:"form_refs,omitempty"`
	QuestionToks int      `json:"question_tokens"`
}

type DecideResponse struct {
	Provider   string       `json:"provider"`
	Model      string       `json:"model"`
	API        string       `json:"api"`
	Kind       DecideKind   `json:"kind"`
	Question   string       `json:"question"`
	Rows       []DecideRow  `json:"rows"`
	Confidence *float64     `json:"confidence,omitempty"`
	Window     DecideWindow `json:"window"`
	InputToks  int          `json:"input_tokens"`
	OutputToks int          `json:"output_tokens"`
}
