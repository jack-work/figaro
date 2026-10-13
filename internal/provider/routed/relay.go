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
	}
	return ""
}
