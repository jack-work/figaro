package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"go.opentelemetry.io/otel/attribute"
	"sort"
	"strings"
	"time"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/quote"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/store"
)

const (
	ProviderKey   = "system.decide.provider"
	ModelKey      = "system.decide.model"
	APIKey        = "system.decide.api"
	BaseURLKey    = "system.decide.base_url"
	StateLimit    = systemOneLimit
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
	Log      store.Log[message.Message]
	Board    form.Snapshot
	Defaults form.Snapshot
	Context  string
}

type Target struct {
	BaseURL string
	Auth    auth.TokenResolver
}

type Targets func(provider string) (Target, error)

type Engine struct {
	targets Targets
}

func New(targets Targets) *Engine { return &Engine{targets: targets} }

type settings struct {
	provider, model, api, baseURL string
}

func (src Source) settings(req rpc.DecideRequest) (settings, error) {
	pick := func(asked, key string) string {
		return firstOf(asked, boardString(src.Board, key), boardString(src.Defaults, key))
	}
	s := settings{
		provider: pick(req.Provider, ProviderKey),
		model:    pick(req.Model, ModelKey),
		api:      pick(req.API, APIKey),
		baseURL:  pick("", BaseURLKey),
	}
	var missing []string
	if s.provider == "" {
		missing = append(missing, "provider")
	}
	if s.model == "" {
		missing = append(missing, "model")
	}
	if len(missing) > 0 {
		return s, fmt.Errorf("no decision %s: set them under [system.decide] in an outfit (core.toml reaches every aria), or pass --provider and -m",
			strings.Join(missing, " or "))
	}
	return s, nil
}

type plan struct {
	ask     string
	options []option
	refs    []string
	kind    rpc.DecideKind
	focus   *quote.Range
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
	case len(opts) == 1:
		return p, fmt.Errorf("one option (%s) is not a choice: give two or more, or none for a yes/no", opts[0].label)
	default:
		p.kind = rpc.DecideChoice
	}
	return p, nil
}

func (e *Engine) Decide(ctx context.Context, src Source, req rpc.DecideRequest) (out rpc.DecideResponse, err error) {
	ctx, span := figOtel.Start(ctx, "decide")
	defer func() {
		if err != nil {
			figOtel.RecordError(ctx, "decide.error", err)
		} else {
			span.SetAttributes(
				attribute.String("decide.answered_by", out.Model),
				attribute.String("decide.kind", string(out.Kind)),
				attribute.Int("decide.window.messages", out.Window.Messages),
				attribute.Int("decide.window.tokens", out.Window.Tokens),
				attribute.Int("decide.input_tokens", out.InputToks),
			)
		}
		span.End()
	}()
	p, err := parse(req, src.Board)
	if err != nil {
		return out, err
	}
	set, err := src.settings(req)
	if err != nil {
		return out, err
	}
	model, api := set.model, set.api
	span.SetAttributes(attribute.String("decide.provider", set.provider), attribute.String("decide.model", model))
	if api == "" {
		api = apidecide.APIFor(model)
	} else if api, err = apidecide.ParseAPI(api); err != nil {
		return out, err
	}
	span.SetAttributes(attribute.String("decide.api", api))
	d, err := dialectFor(api)
	if err != nil {
		return out, err
	}

	usable := d.limit() * usableShare / usableOf
	qTokens := estimate(d.question(p))
	if qTokens+minimumState > usable {
		return out, fmt.Errorf("the question and its options are ~%d tokens; %s (%s) accepts %d for the context and the question together, so there is no room left to send them whole",
			qTokens, model, api, d.limit())
	}
	avail := usable - qTokens - envelope
	if req.Budget > 0 && req.Budget < avail {
		avail = req.Budget
	}
	_, targetSpan := figOtel.Start(ctx, "decide.target", figOtel.WithAttributes(attribute.String("decide.provider", set.provider)))
	target, err := e.targets(set.provider)
	targetSpan.End()
	if err != nil {
		return out, fmt.Errorf("decision provider %q: %w", set.provider, err)
	}
	client := NewClient(firstOf(set.baseURL, target.BaseURL), target.Auth)

	var resp response
	var w built
	var stateTokens int
	for attempt := 0; ; attempt++ {
		w, err = build(src, p, avail)
		if err != nil {
			return out, err
		}
		var body any
		if body, stateTokens, err = d.body(model, w.state, p); err != nil {
			return out, err
		}
		var raw []byte
		postCtx, postSpan := figOtel.Start(ctx, "decide.post", figOtel.WithAttributes(
			attribute.Int("decide.attempt", attempt+1), attribute.Int("decide.window.tokens", stateTokens)))
		posted := time.Now()
		raw, err = client.post(postCtx, d.path(), body)
		figOtel.RecordDecideUpstream(ctx, time.Since(posted), attribute.String("decide.api", api), attribute.Bool("decide.ok", err == nil))
		postSpan.End()
		if err == nil {
			resp, err = d.decode(raw)
		}
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
	out.Model = firstOf(resp.Model, model)
	out.API = api
	out.Provider = set.provider
	out.Kind, out.Question = p.kind, p.ask
	out.Rows = rank(p, a)
	out.Confidence = a.Confidence
	out.Window.Messages = w.messages
	out.Window.Tokens = stateTokens
	out.Window.Budget = avail
	out.Window.Focus = w.focus
	out.Window.FormRefs = p.refs
	out.Window.QuestionToks = qTokens
	if resp.Usage != nil {
		out.InputToks, out.OutputToks = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	figOtel.RecordDecideWindow(ctx, stateTokens, attribute.String("decide.api", api))
	return out, nil
}

type built struct {
	state    state
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
	b.state, b.messages = s, len(lines)
	return b, nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func boardString(board form.Snapshot, key string) string {
	if v := board.Lookup(key); v != nil {
		return *v
	}
	return ""
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
