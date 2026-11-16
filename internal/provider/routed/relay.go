package routed

import (
	"strings"

	"github.com/jack-work/figaro/internal/auth"
)

func (p *Provider) Base() string { return p.base() }

func (p *Provider) Endpoint(t Transport) string {
	switch t {
	case Messages:
		return p.base() + p.spec.MessagesPath
	case Chat:
		return p.base() + "/chat/completions"
	case FigIR:
		return p.base() + "/fig-ir"
	case CodeAssist:
		return p.base() + "/v1internal:streamGenerateContent?alt=sse"
	case Gemini:
		return p.base() + "/models"
	}
	return p.base() + "/responses"
}

func (p *Provider) Headers(t Transport) map[string]string { return p.spec.headersFor(t) }

func (p *Provider) Credential() auth.TokenResolver { return p.resolver }

func TransportForPath(path string) Transport {
	path = strings.TrimRight(path, "/")
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return Chat
	case strings.HasSuffix(path, "/messages"):
		return Messages
	case strings.HasSuffix(path, "/responses"):
		return Responses
	case strings.HasSuffix(path, "/fig-ir"):
		return FigIR
	case strings.Contains(path, "v1internal:"):
		return CodeAssist
	case strings.Contains(path, ":streamGenerateContent") || strings.Contains(path, ":generateContent"):
		return Gemini
	}
	return ""
}
