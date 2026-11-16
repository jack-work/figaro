package figir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/catalog"
	"github.com/jack-work/figaro/internal/catalogmaps"
	figOtel "github.com/jack-work/figaro/internal/otel"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/upstream"
	"github.com/jack-work/figaro/internal/wirelog"
)

var defaultFormTemplates = sync.OnceValue(func() *template.Template {
	t, _ := form.LoadDefaultTemplates()
	return t
})

type Provider struct {
	auth       auth.TokenResolver
	mu         sync.Mutex
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
	Route      provider.Route
	Templates  *template.Template
	Catalog    *catalog.Reader
}

func New(knobs provider.Knobs, resolver auth.TokenResolver, route provider.Route) (*Provider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("figir: nil token resolver")
	}
	if route.BaseURL == "" {
		return nil, fmt.Errorf("figir: route has no base URL")
	}
	if route.Dialect == "" {
		route.Dialect = provider.DialectFigIR
	}
	return &Provider{
		auth:       resolver,
		Model:      knobs.Model,
		MaxTokens:  knobs.MaxTokens,
		HTTPClient: &http.Client{Transport: &wirelog.Transport{Inner: upstream.Shared()}},
		Route:      route,
	}, nil
}

func (p *Provider) Name() string { return p.Route.Name }

func (p *Provider) Fingerprint() string { return "fig-ir/" + p.Route.Name + "/v1" }

func (p *Provider) SetModel(model string) {
	p.mu.Lock()
	p.Model = model
	p.mu.Unlock()
}

func (p *Provider) SetTemplates(t *template.Template) {
	p.mu.Lock()
	p.Templates = t
	p.mu.Unlock()
}

func (p *Provider) templates() *template.Template {
	p.mu.Lock()
	t := p.Templates
	p.mu.Unlock()
	if t != nil {
		return t
	}
	return defaultFormTemplates()
}

func (p *Provider) resolveModel(snap form.Snapshot) string {
	if v := snap.Lookup("system.model"); v != nil {
		return p.Route.QualifyModel(*v)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Route.QualifyModel(p.Model)
}

func (p *Provider) credential() auth.TokenResolver {
	if p.Route.AuthOptional {
		return auth.Optional(p.auth)
	}
	return p.auth
}

func (p *Provider) authorize(req *http.Request, token string) {
	for k, v := range p.Route.Headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (p *Provider) catalogReader() (*catalog.Reader, error) {
	if p.Catalog != nil {
		return p.Catalog, nil
	}
	return catalog.New(catalogmaps.Builtin("generic"), nil)
}

func (p *Provider) SeedModel(model string) (models.Entry, bool) {
	r, err := p.catalogReader()
	if err != nil || r == nil {
		return models.Entry{}, false
	}
	return r.Seed(model)
}

func (p *Provider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	resp, err := auth.SendRenewing(p.credential(), func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Route.ModelsURL(), nil)
		if err != nil {
			return nil, err
		}
		p.authorize(req, token)
		return p.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("%s models API %d: %s", p.Route.Name, resp.StatusCode, body)
	}
	reader, err := p.catalogReader()
	if err != nil {
		return nil, err
	}
	listed, _, err := reader.Page(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", p.Route.Name, err)
	}
	out := make([]provider.ModelInfo, 0, len(listed))
	for _, m := range listed {
		out = append(out, provider.ModelInfo{ID: m.ID, Provider: p.Route.Name, Entry: m.Entry})
	}
	return out, nil
}

func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) (err error) {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}
	if in.FigLog == nil {
		return fmt.Errorf("empty context")
	}
	if arialog.ViewHidesOwnRows(in.FigLog) {
		return arialog.ErrViewHidesOwnRows
	}

	rows, hasRows, err := p.deriveRows(in.FigLog, in.Form, in.Studies)
	if err != nil {
		return err
	}
	if !hasRows {
		return fmt.Errorf("empty context")
	}

	req, err := p.assembleFrame(in.Snapshot, in.Tools, in.MaxTokens, in.AriaID)
	if err != nil {
		return err
	}

	shaped, err := provider.ShapeRequest(p.Route.Name, rows, in, p.Route.MaxRequestBytes, provider.FigIRImages, func(r provider.RowSeq) provider.BodyFunc {
		return bodyFuncSeq(req, r)
	})
	if err != nil {
		return err
	}
	body, err := provider.NewRequestBody(bodyFuncSeq(req, shaped), provider.StreamsRequestBody(in.Snapshot))
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	figOtel.Event(ctx, "provider.request_built", attribute.Int("bytes", body.Len()))

	ctx, settle := provider.WatchFirstByte(ctx, provider.FirstByteTimeout(in.Snapshot))
	defer func() { err = provider.NameStreamCut(settle(err), p.Route.Name) }()

	sendOnce := func(token string) (*http.Response, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, p.Route.FigIRURL(), nil)
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		body.Attach(httpReq)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if req.SessionID != "" {
			httpReq.Header.Set("x-session-id", req.SessionID)
		}
		p.authorize(httpReq, token)
		provider.StampCaller(ctx, httpReq.Header)
		provider.StampAttemptToken(ctx, httpReq)
		return p.HTTPClient.Do(httpReq)
	}

	var resp *http.Response
	switch token, pinned := provider.AttemptCredential(ctx); {
	case pinned:
		resp, err = sendOnce(token)
	case provider.SingleAttempt(ctx):
		cred := p.credential()
		token, err = cred.Resolve()
		if err == nil {
			resp, err = sendOnce(token)
		}
	default:
		resp, err = auth.SendRenewing(p.credential(), sendOnce)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, provider.FallbackBodyLimit+1))
		if fallback := provider.ParseRouteFallback(resp.StatusCode, resp.Header, errBody); fallback != nil {
			return fallback
		}
		return refusalOf(resp.StatusCode, errBody, fmt.Errorf("%s: %s", p.Route.Name, apiErrorMessage(resp.StatusCode, errBody)))
	}

	out, err := drainSSE(ctx, resp.Body, bus)
	if err != nil {
		partial := out.toIRMessage()
		if !errors.Is(err, context.Canceled) || len(partial.Content) == 0 {
			return err
		}
		partial.StopReason = message.StopAborted
		partial.Timestamp = time.Now().UnixMilli()
		p.handOver(partial, bus)
		return err
	}
	if out.Usage == nil {
		slog.Warn("no usage block in fig-ir response; token accounting for this turn is unavailable",
			"route", p.Route.Name, "model", req.Model)
	}
	msg := out.toIRMessage()
	wireStop := out.WireStop
	if strings.EqualFold(wireStop, "stop") || (wireStop == "" && msg.StopReason == message.StopEnd) {
		wireStop = "stop"
	}
	if fault := provider.CheckCompletion(p.Route.Name, msg, wireStop); fault != nil {
		if len(msg.Content) > 0 {
			msg.Timestamp = time.Now().UnixMilli()
			p.handOver(msg, bus)
		}
		return fault
	}
	msg.Timestamp = time.Now().UnixMilli()
	p.handOver(msg, bus)
	return nil
}

func (p *Provider) handOver(msg message.Message, bus provider.Bus) {
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
}

func (p *Provider) assembleFrame(snap form.Snapshot, tools []provider.Tool, maxTokens int, ariaID string) (Request, error) {
	if maxTokens <= 0 {
		maxTokens = p.MaxTokens
	}
	req := Request{
		Model:     p.resolveModel(snap),
		System:    readCredo(snap),
		Stream:    true,
		MaxTokens: maxTokens,
		SessionID: provider.SessionKey(ariaID),
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	if err := applyKnobs(&req, snap); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (p *Provider) deriveRows(figLog arialog.Log[message.Message], bound provider.Form, studies map[string]provider.Form) (provider.RowSeq, bool, error) {
	tmpls := p.templates()
	encodeOne := func(msg message.Message, prev form.Snapshot) ([]byte, error) {
		var reminders []string
		form.FoldRender(prev, msg.Patches, tmpls,
			func(r form.RenderedEntry) {
				reminders = append(reminders, fmt.Sprintf("<system-reminder name=\"%s\">\n%s\n</system-reminder>", escapeAttr(r.Key), r.Body))
			},
			func(err error) { slog.Warn("figir: render patch", "err", err) },
		)
		reminders = append(reminders, provider.StudyReminderTexts(msg, prev)...)
		reminders = append(reminders, provider.ForkReminderTexts(msg, prev)...)
		wm, err := encodeMessage(msg, reminders)
		if err != nil || wm == nil {
			return nil, err
		}
		return json.Marshal(wm)
	}

	seq := func(yield func(json.RawMessage, uint64) bool) {
		d := provider.NewDeriver(bound, studies)
		arialog.ScanAll(figLog, func(entry arialog.Entry[message.Message]) bool {
			msg, snap, translatable := d.Next(entry)
			if !translatable {
				return true
			}
			msg, _ = provider.SendableImages(msg)
			raw, err := encodeOne(msg, snap)
			if err != nil || len(raw) == 0 {
				return true
			}
			d.Commit(entry, msg)
			return yield(json.RawMessage(raw), entry.LT)
		})
	}

	hasRows := false
	for range seq {
		hasRows = true
		break
	}
	return seq, hasRows, nil
}

var imageTooBig = regexp.MustCompile(`image exceeds [0-9.]+ MB maximum: [0-9]+ bytes > ([0-9]+) bytes`)

func refusalOf(status int, body []byte, err error) error {
	if status != http.StatusBadRequest {
		return err
	}
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return err
	}
	if parsed.Error.Code == "unsupported_capability" && strings.Contains(parsed.Error.Message, "carries an image") {
		return &provider.ImagesRefused{Err: err}
	}
	if m := imageTooBig.FindStringSubmatch(parsed.Error.Message); m != nil {
		limit, _ := strconv.Atoi(m[1])
		return &provider.ImageRefused{Err: err, MaxBase64: limit}
	}
	return err
}

func apiErrorMessage(status int, body []byte) string {
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		if parsed.Error.Type != "" {
			return fmt.Sprintf("%s: %s (%d)", parsed.Error.Type, parsed.Error.Message, status)
		}
		return fmt.Sprintf("%s (%d)", parsed.Error.Message, status)
	}
	if len(body) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, strings.TrimSpace(string(body)))
}
