package copilot

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider/responses"
	"github.com/jack-work/figaro/internal/provider/routed"
)

const (
	providerName      = "copilot"
	defaultBaseURL    = "https://api.individual.githubcopilot.com"
	directBaseURL     = "https://api.enterprise.githubcopilot.com"
	copilotAPIVersion = "2026-06-01"
)

var copilotStaticHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

var copilotGitHubHeaders = map[string]string{
	"X-GitHub-Api-Version": copilotAPIVersion,
}

func Spec(tokens *CopilotTokenSource) routed.Spec {
	headers := map[string]string{
		"Openai-Intent": "conversation-edits",
		"X-Initiator":   "user",
	}
	for k, v := range copilotStaticHeaders {
		headers[k] = v
	}
	for k, v := range copilotGitHubHeaders {
		headers[k] = v
	}
	return routed.Spec{
		Name:    providerName,
		BaseURL: tokens.BaseURL,
		Headers: headers,
		TransportHeaders: map[routed.Transport]map[string]string{
			routed.Messages: {"anthropic-dangerous-direct-browser-access": "true"},
		},
		IdentityHeaders: identityHeaders,
		Catalog:         "/models",
		Rules:           []routed.Rule{{Model: "claude-*", Transport: routed.Messages}},
	}
}

func identityHeaders(ids responses.IDs) map[string]string {
	return map[string]string{
		"X-Agent-Task-Id":          ids.Task,
		"X-Client-Machine-Id":      ids.Machine,
		"X-Client-Session-Id":      ids.Session,
		"X-Interaction-Id":         ids.Interaction,
		"X-Interaction-Type":       "user",
		"X-Github-Repository-Host": "github.com",
		"X-Github-Repository-Nwo":  "",
	}
}

func baseURLFromToken(token, enterpriseDomain string) string {
	start := bytes.Index([]byte(token), []byte("proxy-ep="))
	if start >= 0 {
		start += len("proxy-ep=")
		end := bytes.IndexByte([]byte(token)[start:], ';')
		var host string
		if end < 0 {
			host = string([]byte(token)[start:])
		} else {
			host = string([]byte(token)[start : start+end])
		}
		if len(host) > 6 && host[:6] == "proxy." {
			host = "api." + host[6:]
		}
		return "https://" + host
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return defaultBaseURL
}

type CopilotTokenSource struct {
	resolver     auth.TokenResolver
	domain       string
	direct       bool
	baseOverride string
}

func newTokenSource(resolver auth.TokenResolver, cfg Config) *CopilotTokenSource {
	return &CopilotTokenSource{
		resolver:     resolver,
		domain:       cfg.EnterpriseDomain,
		direct:       strings.EqualFold(strings.TrimSpace(cfg.TokenMode), "direct"),
		baseOverride: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
	}
}

// BaseURL: explicit override, then the routing hush minted with the token,
// then the derivation from the token body.
func (s *CopilotTokenSource) BaseURL() string {
	if s.baseOverride != "" {
		return s.baseOverride
	}
	if s.direct {
		return directBaseURL
	}
	carrier, _ := s.resolver.(auth.EndpointCarrier)
	if carrier != nil {
		if api := carrier.Endpoint(); api != "" {
			return api
		}
	}
	// Resolving the token fetches the routing alongside it.
	tok, err := s.resolver.Resolve()
	if err != nil {
		return defaultBaseURL
	}
	if carrier != nil {
		if api := carrier.Endpoint(); api != "" {
			return api
		}
	}
	return baseURLFromToken(tok, s.domain)
}

func (s *CopilotTokenSource) Resolve() (string, error) {
	tok, err := s.resolver.Resolve()
	if err != nil {
		return "", fmt.Errorf("copilot: resolve token: %w", err)
	}
	return tok, nil
}

func (s *CopilotTokenSource) Invalidate(token string) error {
	return s.resolver.Invalidate(token)
}
