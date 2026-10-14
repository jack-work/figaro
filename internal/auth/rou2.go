package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

const Rou2WellKnown = "/.well-known/rou2"

type Rou2Upstream struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Default bool   `json:"default"`
}

type Rou2Discovery struct {
	Service       string         `json:"service"`
	Gate          string         `json:"gate"`
	Issuer        string         `json:"issuer"`
	ClientID      string         `json:"client_id"`
	Scopes        []string       `json:"scopes"`
	OIDCDiscovery string         `json:"oidc_discovery"`
	Upstreams     []Rou2Upstream `json:"upstreams"`
}

type oidcConfiguration struct {
	Issuer                      string `json:"issuer"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

var rou2HTTP = &http.Client{Timeout: 15 * time.Second}

func Rou2Root(raw string) (string, error) {
	root := strings.TrimRight(strings.TrimSpace(raw), "/")
	root = strings.TrimSuffix(root, "/v1")
	u, err := url.Parse(root)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("rou2: %q is not a rou2 address, e.g. http://100.103.40.20:9110", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func DiscoverRou2(root string) (Rou2Discovery, DeviceCodeConfig, error) {
	var d Rou2Discovery
	if err := getJSON(root+Rou2WellKnown, &d); err != nil {
		return d, DeviceCodeConfig{}, fmt.Errorf("rou2 discovery: %w", err)
	}
	if d.Service != "rou2" {
		return d, DeviceCodeConfig{}, fmt.Errorf("rou2 discovery: %s%s does not describe a rou2 (service %q)", root, Rou2WellKnown, d.Service)
	}
	if d.Gate != "oidc" {
		return d, DeviceCodeConfig{}, fmt.Errorf("rou2: gate is %q, so there is nothing to log in to", d.Gate)
	}
	cfg, err := d.DeviceConfig()
	return d, cfg, err
}

func (d Rou2Discovery) DeviceConfig() (DeviceCodeConfig, error) {
	issuer := strings.TrimRight(d.Issuer, "/")
	iu, err := url.Parse(issuer)
	if err != nil || iu.Scheme != "https" || iu.Host == "" {
		return DeviceCodeConfig{}, fmt.Errorf("rou2: issuer %q is not an https URL", d.Issuer)
	}
	if d.ClientID == "" {
		return DeviceCodeConfig{}, fmt.Errorf("rou2: discovery names no client_id")
	}
	disc := d.OIDCDiscovery
	if disc == "" {
		disc = issuer + "/.well-known/openid-configuration"
	}
	if !sameOrigin(disc, iu) {
		return DeviceCodeConfig{}, fmt.Errorf("rou2: oidc_discovery %q is not on the issuer %s", disc, issuer)
	}
	var oc oidcConfiguration
	if err := getJSON(disc, &oc); err != nil {
		return DeviceCodeConfig{}, fmt.Errorf("oidc discovery: %w", err)
	}
	if strings.TrimRight(oc.Issuer, "/") != issuer {
		return DeviceCodeConfig{}, fmt.Errorf("rou2: %s names issuer %q, rou2 names %q", disc, oc.Issuer, d.Issuer)
	}
	if oc.DeviceAuthorizationEndpoint == "" {
		return DeviceCodeConfig{}, fmt.Errorf("rou2: %s has no device_authorization_endpoint", issuer)
	}
	for _, e := range []string{oc.DeviceAuthorizationEndpoint, oc.TokenEndpoint} {
		if !sameOrigin(e, iu) {
			return DeviceCodeConfig{}, fmt.Errorf("rou2: endpoint %q is not on the issuer %s", e, issuer)
		}
	}
	return DeviceCodeConfig{
		ProviderName: "rou2",
		ClientID:     d.ClientID,
		Scope:        rou2Scope(d.Scopes),
		DeviceURL:    oc.DeviceAuthorizationEndpoint,
		TokenURL:     oc.TokenEndpoint,
	}, nil
}

func rou2Scope(scopes []string) string {
	var parts []string
	for _, s := range scopes {
		if s = strings.TrimSpace(s); s != "" && s != offlineAccess {
			parts = append(parts, s)
		}
	}
	return strings.Join(append(parts, offlineAccess), " ")
}

func sameOrigin(raw string, want *url.URL) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == want.Scheme && u.Host == want.Host
}

func getJSON(endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := rou2HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 256)])))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s: %w", endpoint, err)
	}
	return nil
}

func LoginRou2(cfg DeviceCodeConfig, issuer string) (DeviceTokens, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("rou2 signs in with "+issuer+" (device flow)."))
	fmt.Fprintln(os.Stderr)
	tok, err := LoginDeviceCode(cfg, printDevicePrompt)
	if err != nil {
		return DeviceTokens{}, err
	}
	if tok.RefreshToken == "" {
		return DeviceTokens{}, fmt.Errorf("rou2: %s issued no refresh token, so the login would end with this access token", issuer)
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized by "+issuer)
	return tok, nil
}
