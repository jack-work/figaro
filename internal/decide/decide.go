package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/store"
)

const (
	ModelKey      = "system.decide.model"
	StateLimit    = 32768
	usableShare   = 9
	usableOf      = 10
	envelope      = 64
	minimumState  = 256
	questionKey   = "decision"
	choiceDefault = "Which option fits best?"
	noulFloor     = 0.5
	retryShare    = 6
	retryOf       = 10
)

type Source struct {
	Log     store.Log[message.Message]
	Board   form.Snapshot
	Context string
}

type Engine struct {
	settings func() (Settings, error)
	resolver auth.TokenResolver
}

func New(settings func() (Settings, error), resolver auth.TokenResolver) *Engine {
	return &Engine{settings: settings, resolver: resolver}
}

func Fixed(baseURL, model string) func() (Settings, error) {
	return func() (Settings, error) {
		if model == "" {
			model = DefaultModel
		}
		return Settings{BaseURL: baseURL, Model: model}, nil
	}
}

type plan struct {
	ask      string
	options  []option
	refs     []string
	kind     rpc.DecideKind
	focus    *quote.Range
	question question
}

func parse(req rpc.DecideRequest, board form.Snapshot) (plan, error) {
	text := strings.TrimSpace(req.Question)
	var p plan
	if r, rest, err := quote.Parse(text); err == nil {
		p.focus = &r
		text = strings.TrimSpace(rest)
	} else if !errors.Is(err, quote.ErrNone) {
		return p, err
	}
	body, choices, ok := apidecide.Parse(text)
	if !ok {
		body = text
		if bare, isBare := apidecide.Bare(text); isBare {
			body = bare
		}
	}
	choices = apidecide.Dedupe(append(choices, req.Choices...))
	opts, refs, err := expand(choices, board)
	if err != nil {
		return p, err
	}
	p.ask, p.options, p.refs = strings.TrimSpace(body), opts, refs
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.label
	}
	switch {
	case len(opts) == 0, len(refs) == 0 && apidecide.YesNo(labels):
		p.kind = rpc.DecideYesNo
		if p.ask == "" {
			return p, errors.New("nothing to decide: the question is empty")
		}
		p.question = noulQuestion(p.ask)
	case len(opts) == 1:
		return p, fmt.Errorf("one option (%s) is not a choice: give two or more, or none for a yes/no", opts[0].label)
	default:
		p.kind = rpc.DecideChoice
		ask := p.ask
		if ask == "" {
			ask = choiceDefault
		}
		p.question = choiceQuestion(ask, opts)
	}
	return p, nil
}

func (e *Engine) Decide(ctx context.Context, src Source, req rpc.DecideRequest) (rpc.DecideResponse, error) {
	var out rpc.DecideResponse
	p, err := parse(req, src.Board)
	if err != nil {
		return out, err
	}
	settings, err := e.settings()
	if err != nil {
		return out, err
	}
	usable := StateLimit * usableShare / usableOf
	qTokens := estimate(p.question)
	if qTokens+minimumState > usable {
		return out, fmt.Errorf("the question and its options are ~%d tokens; %s accepts %d for the state and the question together, so there is no room left to send them whole",
			qTokens, Name, StateLimit)
	}
	avail := usable - qTokens - envelope
	if req.Budget > 0 && req.Budget < avail {
		avail = req.Budget
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		if v := src.Board.Lookup(ModelKey); v != nil {
			model = strings.TrimSpace(*v)
		}
	}
	if model == "" {
		model = settings.Model
	}
	client := NewClient(settings.BaseURL, e.resolver)

	var resp response
	var w built
	for attempt := 0; ; attempt++ {
		w, err = build(src, p, avail)
		if err != nil {
			return out, err
		}
		resp, err = client.post(ctx, request{
			Model:     model,
			State:     w.state,
			Questions: map[string]question{questionKey: p.question},
		})
		var he *httpError
		if err == nil || attempt > 0 || !errors.As(err, &he) || !he.tooBig() {
			break
		}
		avail = avail * retryShare / retryOf
	}
	if err != nil {
		return out, err
	}
	a, ok := resp.Answers[questionKey]
	if !ok {
		return out, fmt.Errorf("%s: the response carries no answer", Name)
	}
	out.Model = resp.Model
	if out.Model == "" {
		out.Model = model
	}
	out.Kind, out.Question = p.kind, p.ask
	out.Rows = rank(p, a)
	out.Confidence = a.Confidence
	out.Window.Messages = w.messages
	out.Window.Tokens = tokensOf(string(w.state))
	out.Window.Budget = avail
	out.Window.Focus = w.focus
	out.Window.FormRefs = p.refs
	out.Window.QuestionToks = qTokens
	if resp.Usage != nil {
		out.InputToks, out.OutputToks = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	return out, nil
}

type built struct {
	state    json.RawMessage
	messages int
	focus    string
}

func build(src Source, p plan, avail int) (built, error) {
	var s state
	var b built
	var centre, centreEnd uint64
	if p.focus != nil {
		text, err := focusText(src.Log, *p.focus)
		if err != nil {
			return b, err
		}
		s.Focus = clip(text, avail/2*bytesPerTokenNum/bytesPerTokenDen)
		avail -= tokensOf(s.Focus)
		centre, centreEnd = p.focus.Start.LT, p.focus.End.LT
		b.focus = quote.Format(*p.focus)
	}
	if c := strings.TrimSpace(src.Context); c != "" {
		s.Context = clip(c, avail*bytesPerTokenNum/bytesPerTokenDen)
		avail -= tokensOf(s.Context)
	}
	lines := window(src.Log, avail, centre, centreEnd)
	for _, l := range lines {
		s.Conversation = append(s.Conversation, l.text)
	}
	raw, err := encodeState(s, p.ask)
	if err != nil {
		return b, err
	}
	b.state, b.messages = raw, len(lines)
	return b, nil
}

func encodeState(s state, ask string) (json.RawMessage, error) {
	if s.empty() {
		return json.Marshal(ask)
	}
	return json.Marshal(s)
}

func rank(p plan, a answer) []rpc.DecideRow {
	if p.kind == rpc.DecideYesNo {
		if a.Noul == nil {
			return nil
		}
		yes := rpc.DecideRow{Option: "yes", Probability: *a.Noul, Chosen: *a.Noul >= noulFloor}
		no := rpc.DecideRow{Option: "no", Probability: 1 - *a.Noul, Chosen: *a.Noul < noulFloor}
		if yes.Chosen {
			return []rpc.DecideRow{yes, no}
		}
		return []rpc.DecideRow{no, yes}
	}
	order := make(map[string]int, len(p.options))
	for i, o := range p.options {
		order[o.label] = i
	}
	rows := make([]rpc.DecideRow, 0, len(p.options))
	for _, o := range p.options {
		rows = append(rows, rpc.DecideRow{Option: o.label, Probability: a.Probabilities[o.label], Chosen: o.label == a.Choice})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Probability != rows[j].Probability {
			return rows[i].Probability > rows[j].Probability
		}
		return order[rows[i].Option] < order[rows[j].Option]
	})
	return rows
}
