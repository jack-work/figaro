package routed

import (
	"fmt"
	"path"
	"strings"

	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/responses"
)

type Transport string

const (
	Messages    Transport = "messages"
	Chat        Transport = "chat"
	Responses   Transport = "responses"
	ResponsesWS Transport = "responses-ws"
)

func ParseTransport(s string) (Transport, error) {
	switch t := Transport(strings.ToLower(strings.TrimSpace(s))); t {
	case Messages, Chat, Responses, ResponsesWS:
		return t, nil
	case "":
		return "", nil
	}
	return "", fmt.Errorf("unknown transport %q (want messages, chat, responses or responses-ws)", s)
}

type Rule struct {
	Model     string
	Transport Transport
}

type Spec struct {
	Name         string
	BaseURL      func() string
	AuthOptional bool

	Headers          map[string]string
	TransportHeaders map[Transport]map[string]string
	IdentityHeaders  func(responses.IDs) map[string]string

	Catalog      string
	Default      Transport
	Rules        []Rule
	MessagesPath string

	ModelPrefix string
	Caps        provider.CacheCaps
	MarkMode    provider.MarkMode
}

func (s Spec) Validate() error {
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
	rank := map[Transport]int{ResponsesWS: 4, Responses: 3, Messages: 2, Chat: 1}
	best := Transport("")
	for _, e := range endpoints {
		e = strings.ToLower(strings.TrimSpace(e))
		var t Transport
		switch {
		case strings.Contains(e, "responses") && (strings.HasPrefix(e, "ws:") || strings.HasPrefix(e, "wss:")):
			t = ResponsesWS
		case strings.Contains(e, "responses"):
			t = Responses
		case strings.Contains(e, "messages"):
			t = Messages
		case strings.Contains(e, "chat/completions"):
			t = Chat
		}
		if rank[t] > rank[best] {
			best = t
		}
	}
	return best
}
