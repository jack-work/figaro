package routed

import (
	"bytes"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"text/template"

	"github.com/jack-work/figaro/internal/catalog"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/gemini"
	"github.com/jack-work/figaro/internal/provider/responses"
)

type Transport string

type Dialect = Transport

const (
	Messages    Transport = "messages"
	Chat        Transport = "chat"
	Responses   Transport = "responses"
	ResponsesWS Transport = "responses-ws"
	Gemini      Transport = "gemini"
	CodeAssist  Transport = "code-assist"
)

func ParseTransport(s string) (Transport, error) {
	switch t := Transport(strings.ToLower(strings.TrimSpace(s))); t {
	case Messages, Chat, Responses, ResponsesWS, Gemini, CodeAssist:
		return t, nil
	case "code_assist":
		return CodeAssist, nil
	case "":
		return "", nil
	}
	return "", fmt.Errorf("unknown transport %q (want messages, chat, responses, responses-ws, gemini or code-assist)", s)
}

type RoundMode string

const (
	RoundNearest RoundMode = "nearest"
	RoundDown    RoundMode = "down"
	RoundUp      RoundMode = "up"
)

type EffortSpec struct {
	KeepCache             bool
	Ladder                map[string]float64
	Round                 RoundMode
	DefaultRung           string
	MapModel              string
	OutboundModelTemplate string
}

func (e EffortSpec) Empty() bool {
	return !e.KeepCache && len(e.Ladder) == 0 && e.Round == "" &&
		e.DefaultRung == "" && e.MapModel == "" && e.OutboundModelTemplate == ""
}

func (e EffortSpec) Validate() error {
	switch e.Round {
	case "", RoundNearest, RoundDown, RoundUp:
	default:
		return fmt.Errorf("effort.round %q: want nearest, down or up", e.Round)
	}
	for rung, val := range e.Ladder {
		if strings.TrimSpace(rung) == "" {
			return fmt.Errorf("effort.ladder: empty rung name")
		}
		if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 || val > 100 {
			return fmt.Errorf("effort.ladder.%s: %v must be in 0..100", rung, val)
		}
	}
	if e.MapModel != "" {
		re, err := regexp.Compile(e.MapModel)
		if err != nil {
			return fmt.Errorf("effort.map_model: %w", err)
		}
		if re.SubexpIndex("model") < 0 {
			return fmt.Errorf("effort.map_model %q must name (?P<model>...)", e.MapModel)
		}
	}
	if e.OutboundModelTemplate != "" {
		tmpl, err := template.New("outbound_model").Option("missingkey=error").Parse(e.OutboundModelTemplate)
		if err != nil {
			return fmt.Errorf("effort.outbound_model_template: %w", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, struct{ Model, Effort string }{Model: "m", Effort: "high"}); err != nil {
			return fmt.Errorf("effort.outbound_model_template: %w", err)
		}
	}
	return nil
}

type Rule struct {
	Model     string
	Transport Transport
}

type Spec struct {
	Name         string
	BaseURL      func() string
	AuthOptional bool
	AuthStyle    provider.AuthStyle

	Headers          map[string]string
	TransportHeaders map[Transport]map[string]string
	IdentityHeaders  func(responses.IDs) map[string]string
	SendFigaroID     bool
	CodeAssist       gemini.CodeAssistConfig

	Catalog      string
	Mapping      catalog.Mapping
	Overrides    map[string]catalog.Override
	Default      Transport
	Rules        []Rule
	MessagesPath string

	ModelPrefix string
	Caps        provider.CacheCaps
	MarkMode    provider.MarkMode
	TextOnly    bool
	Effort      EffortSpec

	MaxRequestBytes                 int
	ResponsesOmitFields             []string
	ResponsesNoReplay               bool
	ResponsesPromptCacheBreakpoints bool
}

func (s Spec) Validate() error {
	if err := responses.ValidateOmitFields(s.ResponsesOmitFields); err != nil {
		return err
	}
	if s.Name == "" {
		return fmt.Errorf("routed: a provider spec needs a name")
	}
	if s.BaseURL == nil {
		return fmt.Errorf("%s: no base_url", s.Name)
	}
	for _, r := range s.Rules {
		if _, err := path.Match(r.Model, ""); err != nil {
			return fmt.Errorf("%s: routing rule %q: %w", s.Name, r.Model, err)
		}
		if r.Transport == "" {
			return fmt.Errorf("%s: routing rule %q names no transport", s.Name, r.Model)
		}
	}
	if s.Catalog == "" && s.Default == "" && len(s.Rules) == 0 {
		return fmt.Errorf("%s: routing needs a catalog, a default transport or a rule", s.Name)
	}
	if _, err := catalog.New(s.Mapping, s.Overrides); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	if err := s.Effort.Validate(); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	return nil
}

func (s Spec) static() (Transport, bool) {
	if s.Catalog == "" && len(s.Rules) == 0 && s.Default != "" {
		return s.Default, true
	}
	return "", false
}

func (s Spec) ruleFor(model string) (Transport, bool) {
	for _, r := range s.Rules {
		if ok, _ := path.Match(r.Model, model); ok {
			return r.Transport, true
		}
	}
	return "", false
}

func (s Spec) namespace(t Transport) string {
	switch t {
	case Chat:
		return s.Name
	case Messages:
		return s.Name + "-messages"
	case Gemini:
		return s.Name + "-gemini"
	case CodeAssist:
		return s.Name + "-code-assist"
	}
	return s.Name + "-responses"
}

func (s Spec) headersFor(t Transport) map[string]string {
	out := make(map[string]string, len(s.Headers)+len(s.TransportHeaders[t]))
	for k, v := range s.Headers {
		out[k] = v
	}
	for k, v := range s.TransportHeaders[t] {
		out[k] = v
	}
	return out
}

func transportForEndpoints(endpoints []string) Transport {
	rank := map[Transport]int{CodeAssist: 6, Gemini: 5, ResponsesWS: 4, Responses: 3, Messages: 2, Chat: 1}
	best := Transport("")
	for _, e := range endpoints {
		e = strings.ToLower(strings.TrimSpace(e))
		var t Transport
		switch {
		case strings.Contains(e, "v1internal:streamgeneratecontent") || e == "code-assist" || e == "code_assist":
			t = CodeAssist
		case strings.Contains(e, ":streamgeneratecontent") || e == "gemini":
			t = Gemini
		case strings.Contains(e, "responses") && (strings.HasPrefix(e, "ws:") || strings.HasPrefix(e, "wss:")):
			t = ResponsesWS
		case strings.Contains(e, "responses"):
			t = Responses
		case strings.Contains(e, "messages"):
			t = Messages
		case strings.Contains(e, "chat/completions") || e == "chat_completions":
			t = Chat
		}
		if rank[t] > rank[best] {
			best = t
		}
	}
	return best
}
