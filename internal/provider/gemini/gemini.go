package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	auth           auth.TokenResolver
	mu             sync.Mutex
	Model          string
	MaxTokens      int
	HTTPClient     *http.Client
	Route          provider.Route
	CodeAssist     CodeAssistConfig
	CacheOpen      func(aria string) (arialog.Log[[]json.RawMessage], error)
	CacheNamespace string
	cache          arialog.Log[[]json.RawMessage]
	cacheAria      string
	Templates      *template.Template
	Catalog        *catalog.Reader
}

func New(knobs provider.Knobs, resolver auth.TokenResolver, route provider.Route,
	cacheOpen func(aria string) (arialog.Log[[]json.RawMessage], error)) (*Provider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("gemini: nil token resolver")
	}
	if route.BaseURL == "" {
		return nil, fmt.Errorf("gemini: route has no base URL")
	}
	if route.Dialect == "" {
		route.Dialect = provider.DialectGemini
	}
	ns := route.Name + "-" + string(route.Dialect)
	if route.Name == "" {
		ns = string(route.Dialect)
	}
	return &Provider{
		auth:           resolver,
		Model:          knobs.Model,
		MaxTokens:      knobs.MaxTokens,
		HTTPClient:     &http.Client{Transport: &wirelog.Transport{Inner: upstream.Shared()}},
		Route:          route,
		CacheOpen:      cacheOpen,
		CacheNamespace: ns,
	}, nil
}

func (p *Provider) Name() string { return p.Route.Name }

func (p *Provider) Fingerprint() string {
	return string(p.Route.Dialect) + "/" + p.Route.Name + "/v2"
}

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
	if v := snap.Lookup("system.model"); v != nil && strings.TrimSpace(*v) != "" {
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
	if token == "" {
		return
	}
	if p.Route.Auth == provider.AuthGoogleKey && !strings.HasPrefix(token, "ya29.") {
		req.Header.Set("x-goog-api-key", token)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

func (p *Provider) catalogReader() (*catalog.Reader, error) {
	if p.Catalog != nil {
		return p.Catalog, nil
	}
	return catalog.New(catalogmaps.Builtin("gemini"), nil)
}

func (p *Provider) SeedModel(model string) (models.Entry, bool) {
	r, err := p.catalogReader()
	if err != nil || r == nil {
		return models.Entry{}, false
	}
	return r.Seed(model)
}

func (p *Provider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	reader, err := p.catalogReader()
	if err != nil {
		return nil, err
	}
	if p.Route.Dialect == provider.DialectCodeAssist {
		return p.codeAssistModels(ctx, reader)
	}
	return p.generativeModels(ctx, reader)
}

func (p *Provider) codeAssistModels(ctx context.Context, reader *catalog.Reader) ([]provider.ModelInfo, error) {
	resp, err := auth.SendRenewing(p.credential(), func(token string) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, p.Route.CodeAssistModelsURL(), bytes.NewReader([]byte("{}")))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		p.authorize(req, token)
		return p.HTTPClient.Do(req)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("%s models: %s", p.Route.Name, apiErrorMessage(resp.StatusCode, body))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	norm, err := NormalizeCatalogJSON(raw, "/v1internal:streamGenerateContent")
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", p.Route.Name, err)
	}
	listed, _, err := reader.Page(bytes.NewReader(norm))
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", p.Route.Name, err)
	}
	out := make([]provider.ModelInfo, 0, len(listed))
	for _, m := range listed {
		out = append(out, provider.ModelInfo{ID: m.ID, Provider: p.Route.Name, Entry: m.Entry})
	}
	return out, nil
}

func (p *Provider) generativeModels(ctx context.Context, reader *catalog.Reader) ([]provider.ModelInfo, error) {
	baseURL := p.Route.ModelsURL()
	var out []provider.ModelInfo
	seen := map[string]bool{}
	pageToken := ""
	for range 16 {
		target := baseURL
		if pageToken != "" {
			u, uerr := url.Parse(baseURL)
			if uerr != nil {
				return nil, uerr
			}
			q := u.Query()
			q.Set("pageToken", pageToken)
			u.RawQuery = q.Encode()
			target = u.String()
		}
		if seen[target] {
			break
		}
		seen[target] = true

		resp, err := auth.SendRenewing(p.credential(), func(token string) (*http.Response, error) {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if reqErr != nil {
				return nil, reqErr
			}
			req.Header.Set("Accept", "application/json")
			p.authorize(req, token)
			return p.HTTPClient.Do(req)
		})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			return nil, fmt.Errorf("%s models: %s", p.Route.Name, apiErrorMessage(resp.StatusCode, errBody))
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		norm, err := NormalizeCatalogJSON(raw, "/models")
		if err != nil {
			return nil, fmt.Errorf("%s models: %w", p.Route.Name, err)
		}
		listed, _, err := reader.Page(bytes.NewReader(norm))
		if err != nil {
			return nil, fmt.Errorf("%s models: %w", p.Route.Name, err)
		}
		for _, m := range listed {
			out = append(out, provider.ModelInfo{ID: m.ID, Provider: p.Route.Name, Entry: m.Entry})
		}
		var cursor struct {
			NextPageToken string `json:"nextPageToken"`
		}
		if json.Unmarshal(norm, &cursor) != nil || cursor.NextPageToken == "" {
			break
		}
		pageToken = cursor.NextPageToken
	}
	return out, nil
}

func (p *Provider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) (err error) {
	ctx = wirelog.WithAria(ctx, in.AriaID)
	if dir := in.Snapshot.Lookup("system.environment.figaro_wire_dir"); dir != nil && *dir != "" {
		ctx = wirelog.WithLogging(ctx, in.AriaID, *dir)
	}

	cache, err := p.cacheFor(in.AriaID)
	if err != nil {
		return err
	}
	src, err := p.catchUp(ctx, in.FigLog, cache, in.Form, in.Studies)
	if err != nil {
		return err
	}
	if src == nil {
		return fmt.Errorf("empty context")
	}

	model := p.resolveModel(in.Snapshot)
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("%s: missing model", p.Route.Name)
	}
	maxTokens := in.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.MaxTokens
	}
	genCfg, err := buildGenerationConfig(in.Snapshot, maxTokens, p.supportsReasoning(model))
	if err != nil {
		return err
	}
	tools := projectTools(in.Tools)
	credo := readCredo(in.Snapshot)

	makeBody := func(rows provider.RowSeq) provider.BodyFunc {
		paired, sys := pairAndCollectSystem(rows, credo)
		frame := GenerateRequest{
			SystemInstruction: sys,
			Tools:             tools,
			GenerationConfig:  genCfg,
		}
		if p.Route.Dialect == provider.DialectCodeAssist {
			return bodyFuncCodeAssist(model, p.CodeAssist, frame, paired)
		}
		return bodyFuncGemini(frame, paired)
	}

	shaped, err := provider.ShapeRequest(p.Route.Name, src, in, p.Route.MaxRequestBytes, provider.GeminiImages, makeBody)
	if err != nil {
		return err
	}
	body, err := provider.NewRequestBody(makeBody(shaped), provider.StreamsRequestBody(in.Snapshot))
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	figOtel.Event(ctx, "provider.request_built", attribute.Int("bytes", body.Len()))

	endpoint := p.Route.GeminiStreamURL(model)
	if p.Route.Dialect == provider.DialectCodeAssist {
		endpoint = p.Route.CodeAssistStreamURL()
	}
	sessionID := provider.SessionKey(in.AriaID)

	ctx, settle := provider.WatchFirstByte(ctx, provider.FirstByteTimeout(in.Snapshot))
	defer func() { err = provider.NameStreamCut(settle(err), p.Route.Name) }()

	sendOnce := func(token string) (*http.Response, error) {
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		body.Attach(httpReq)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if p.Route.Caps.SessionKey && sessionID != "" {
			httpReq.Header.Set("x-session-id", sessionID)
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
		if perr := p.handOver(partial, bus); perr != nil {
			return perr
		}
		return err
	}
	if out.Usage == nil {
		slog.Warn("no usageMetadata in gemini response; token accounting for this turn is unavailable",
			"route", p.Route.Name, "model", model)
	}
	msg := out.toIRMessage()
	wireStop := out.WireFinish
	if strings.EqualFold(wireStop, "STOP") || (wireStop == "" && msg.StopReason == message.StopEnd) {
		wireStop = "stop"
	}
	if fault := provider.CheckCompletion(p.Route.Name, msg, wireStop); fault != nil {
		if len(msg.Content) > 0 {
			msg.Timestamp = time.Now().UnixMilli()
			_ = p.handOver(msg, bus)
		}
		return fault
	}
	msg.Timestamp = time.Now().UnixMilli()
	return p.handOver(msg, bus)
}

func (p *Provider) handOver(msg message.Message, bus provider.Bus) error {
	bus.PushMessageEnd(string(msg.StopReason))
	native, err := p.assistantCache(msg)
	if err != nil {
		return fmt.Errorf("%s cache assistant: %w", p.Route.Name, err)
	}
	bus.PushFigaro(msg, native)
	return nil
}

func (p *Provider) assistantCache(msg message.Message) (provider.AssistantCache, error) {
	encoded, err := p.encode(msg, form.Snapshot{})
	if err != nil {
		return provider.AssistantCache{}, err
	}
	return provider.AssistantCache{
		Namespace:   p.CacheNamespace,
		Payload:     encoded,
		Fingerprint: p.Fingerprint(),
	}, nil
}

func (p *Provider) encode(msg message.Message, prevSnapshot form.Snapshot) ([]json.RawMessage, error) {
	reminders := p.renderPatches(msg.Patches, prevSnapshot)
	reminders = append(reminders, provider.StudyReminderTexts(msg, prevSnapshot)...)
	reminders = append(reminders, provider.ForkReminderTexts(msg, prevSnapshot)...)
	turns, err := encodeMessage(msg, reminders)
	if err != nil || len(turns) == 0 {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(turns))
	for _, t := range turns {
		raw, merr := json.Marshal(t)
		if merr != nil {
			return nil, fmt.Errorf("marshal gemini content: %w", merr)
		}
		out = append(out, raw)
	}
	return out, nil
}

func (p *Provider) renderPatches(patches []message.Patch, snap form.Snapshot) []string {
	var out []string
	form.FoldRender(snap, patches, p.templates(),
		func(r form.RenderedEntry) {
			out = append(out, fmt.Sprintf("<system-reminder name=\"%s\">\n%s\n</system-reminder>", escapeAttr(r.Key), r.Body))
		},
		func(err error) { slog.Warn("gemini: render patch", "err", err) },
	)
	return out
}

func (p *Provider) EncodeMessage(msg message.Message, prev form.Snapshot) ([]json.RawMessage, error) {
	return p.encode(msg, prev)
}

func (p *Provider) TranslatorChannel() string { return p.CacheNamespace }

func (p *Provider) supportsReasoning(model string) bool {
	if e, ok := p.SeedModel(model); ok && e.Reasoning != nil {
		return *e.Reasoning
	}
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasSuffix(m, "-thinking") ||
		strings.Contains(m, "gemini-2.5") ||
		strings.Contains(m, "gemini-2-5") ||
		strings.Contains(m, "gemini-3") ||
		strings.Contains(m, "gemini-4")
}

func (p *Provider) cacheFor(aria string) (arialog.Log[[]json.RawMessage], error) {
	if aria == "" || p.CacheOpen == nil {
		return arialog.NewMemLog[[]json.RawMessage](), nil
	}
	p.mu.Lock()
	cached := p.cache
	sameAria := p.cacheAria == aria
	p.mu.Unlock()
	if cached != nil && sameAria {
		if !p.invalidateIfStale(cached) {
			return arialog.NewMemLog[[]json.RawMessage](), nil
		}
		return cached, nil
	}
	s, err := p.CacheOpen(aria)
	if err != nil {
		return nil, fmt.Errorf("gemini open translator log: %w", err)
	}
	if s == nil {
		return arialog.NewMemLog[[]json.RawMessage](), nil
	}
	if !p.invalidateIfStale(s) {
		return nil, fmt.Errorf("gemini translator log invalidation failed")
	}
	p.mu.Lock()
	p.cache = s
	p.cacheAria = aria
	p.mu.Unlock()
	return s, nil
}

func (p *Provider) invalidateIfStale(s arialog.Log[[]json.RawMessage]) bool {
	want := p.Fingerprint()
	stored, cleared, err := provider.ClearStaleTranslationCache(s, want)
	if err != nil {
		slog.Warn("gemini clear stale cache", "stored", stored, "current", want, "err", err)
		return false
	}
	if cleared {
		slog.Info("gemini cleared stale cache", "stored", stored, "current", want)
	}
	return true
}

func (p *Provider) catchUp(ctx context.Context, figLog arialog.Log[message.Message], rows arialog.Log[[]json.RawMessage],
	bound provider.Form, studies map[string]provider.Form) (provider.RowSeq, error) {
	if _, err := provider.CatchUp(provider.CatchUpConfig{
		Context:     ctx,
		Log:         figLog,
		Translator:  rows,
		Form:        bound,
		Studies:     studies,
		Phase:       provider.TracePhases(ctx),
		Fingerprint: p.Fingerprint(),
		Provider:    p.Name(),
		Encode:      p.encode,
		ReportEncodeError: func(lt uint64, err error) {
			slog.Warn("gemini encode failed", "lt", lt, "err", err)
		},
		ReportWriteError: func(lt uint64, err error) {
			slog.Warn("gemini write row failed", "lt", lt, "err", err)
		},
		ReportStopped: provider.LogCatchUpStopped(p.Name()),
	}); err != nil {
		return nil, fmt.Errorf("gemini catch up: %w", err)
	}
	to, ok := provider.TranslationTail(rows)
	if !ok {
		return nil, nil
	}
	return provider.TranslationRows(rows, to), nil
}

var (
	imageTooBig = regexp.MustCompile(`image exceeds [0-9.]+ MB maximum: [0-9]+ bytes > ([0-9]+) bytes`)
	secretToken = regexp.MustCompile(`(?i)(bearer\s+[A-Za-z0-9._~+/=-]+|ya29\.[A-Za-z0-9._-]+|AIza[0-9A-Za-z_-]{10,})`)
)

func redactErrorText(s string) string {
	return secretToken.ReplaceAllString(s, "[REDACTED]")
}

func refusalOf(status int, body []byte, err error) error {
	if status != http.StatusBadRequest {
		return err
	}
	var parsed struct {
		Error struct {
			Code    any    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return err
	}
	msgLower := strings.ToLower(parsed.Error.Message)
	if strings.Contains(msgLower, "does not support image") || strings.Contains(msgLower, "multimodal") || strings.Contains(msgLower, "carries an image") {
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
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		msg := redactErrorText(parsed.Error.Message)
		if parsed.Error.Status != "" {
			return fmt.Sprintf("%s: %s (%d)", parsed.Error.Status, msg, status)
		}
		return fmt.Sprintf("%s (%d)", msg, status)
	}
	snippet := redactErrorText(strings.TrimSpace(string(body)))
	if len(snippet) > 512 {
		snippet = snippet[:512] + "..."
	}
	if snippet == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, snippet)
}
