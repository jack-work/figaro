package anthropicsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// OAuth tokens are issued via the Claude Pro/Max OAuth flow. They
// require a different header shape than API keys (Authorization
// instead of x-api-key, plus Claude Code identity headers).
const claudeCodeVersion = "2.1.62"

// Anthropic-beta values declared by the existing implementation.
// Kept in sync so OAuth-bound tokens see the same flags.
const (
	betaMessages = "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,prompt-caching-2024-07-31"
	betaModels   = "claude-code-20250219,oauth-2025-04-20"
)

func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

// authOptions builds the per-request option list for a resolved token.
// Includes credential headers, Anthropic-beta flags, and our HTTP
// client so wirelog is in the chain.
func (p *Provider) authOptions(token, betas string) []option.RequestOption {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(p.httpClient),
	}
	if isOAuthToken(token) {
		// Drop the SDK's default x-api-key (set to "" via WithoutEnvironmentDefaults
		// but defensive in case the SDK adds one elsewhere), inject the
		// Claude Code identity headers, and use the Bearer token.
		opts = append(opts,
			option.WithHeaderDel("x-api-key"),
			option.WithAuthToken(token),
			option.WithHeader("User-Agent", "claude-cli/"+claudeCodeVersion),
			option.WithHeader("x-app", "cli"),
			option.WithHeader("anthropic-dangerous-direct-browser-access", "true"),
		)
	} else {
		opts = append(opts, option.WithAPIKey(token))
	}
	if betas != "" {
		opts = append(opts, option.WithHeader("anthropic-beta", betas))
	}
	return opts
}

// callWithAuthRetry resolves a token, runs do, and on 401 invalidates
// the token and retries once.
func (p *Provider) callWithAuthRetry(ctx context.Context, do func(opts []option.RequestOption) error) error {
	token, err := p.resolver.Resolve()
	if err != nil {
		return fmt.Errorf("resolve token: %w", err)
	}
	err = do(p.authOptions(token, betaMessages))
	if err == nil {
		return nil
	}
	if !isUnauthorized(err) {
		return err
	}
	p.resolver.Invalidate(token)
	fresh, rerr := p.resolver.Resolve()
	if rerr != nil {
		return fmt.Errorf("resolve after 401: %w", rerr)
	}
	if fresh == token {
		return fmt.Errorf("anthropicsdk 401: token unchanged after invalidate")
	}
	return do(p.authOptions(fresh, betaMessages))
}

func isUnauthorized(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 401
	}
	return false
}
