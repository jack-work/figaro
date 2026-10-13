package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	apidecide "github.com/jack-work/figaro/api/decide"
	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/decide"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

type decisions struct {
	base string
	cred auth.TokenResolver
	http *http.Client
}

func Decisions(loaded *config.Loaded) (Upstream, error) {
	settings, err := decide.LoadSettings(loaded)
	if err != nil {
		return nil, err
	}
	resolver, err := boot.KeyResolver(loaded, decide.Name, decide.CredentialEnvs)
	if err != nil {
		return nil, err
	}
	return newDecisions(settings.BaseURL, resolver, settings.BaseURL != decide.DefaultBaseURL)
}

func newDecisions(base string, cred auth.TokenResolver, keyOptional bool) (Upstream, error) {
	if keyOptional {
		cred = optionalKey{cred}
	} else if _, err := cred.Resolve(); err != nil {
		return nil, err
	}
	return &decisions{
		base: strings.TrimRight(base, "/"),
		cred: cred,
		http: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (d *decisions) Base() string { return d.base }

func (d *decisions) Endpoint(routed.Transport) string { return systemOne(d) }

func (d *decisions) Headers(routed.Transport) map[string]string { return nil }

func (d *decisions) Credential() auth.TokenResolver { return d.cred }

func (d *decisions) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+"/models", nil)
	if err != nil {
		return nil, err
	}
	if tok, err := d.cred.Resolve(); err == nil && tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s/models: HTTP %d: %s", d.base, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var catalog struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, err
	}
	var out []provider.ModelInfo
	for _, m := range catalog.Models {
		out = append(out, provider.ModelInfo{ID: m.Name, Name: m.Name, Provider: decide.Name, ContextWindow: decide.StateLimit})
	}
	for _, m := range catalog.Data {
		if apidecide.IsModel(m.ID) {
			out = append(out, provider.ModelInfo{ID: m.ID, Name: m.ID, Provider: decide.Name, ContextWindow: decide.StateLimit})
		}
	}
	return out, nil
}

type optionalKey struct{ inner auth.TokenResolver }

func (o optionalKey) Resolve() (string, error) {
	tok, err := o.inner.Resolve()
	if err != nil {
		return "", nil
	}
	return tok, nil
}

func (o optionalKey) Invalidate(tok string) error { return o.inner.Invalidate(tok) }
