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

const FlowDevice = "device"

type OAuthSettings struct {
	Issuer    string   `toml:"issuer,omitempty"`
	ClientID  string   `toml:"client_id,omitempty"`
	Scopes    []string `toml:"scopes,omitempty"`
	Flow      string   `toml:"flow,omitempty"`
	Discovery string   `toml:"discovery,omitempty"`
}

type oidcConfiguration struct {
	Issuer                      string `json:"issuer"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

var oidcHTTP = &http.Client{Timeout: 15 * time.Second}

func (s OAuthSettings) Discovered() (OAuthSettings, error) {
	if s.Discovery == "" {
		return s, nil
	}
	var doc struct {
		Issuer   string   `json:"issuer"`
		ClientID string   `json:"client_id"`
		Scopes   []string `json:"scopes"`
	}
	if err := getJSON(s.Discovery, &doc); err != nil {
		return s, fmt.Errorf("oauth discovery: %w", err)
	}
	if s.Issuer == "" {
		s.Issuer = doc.Issuer
	}
	if s.ClientID == "" {
		s.ClientID = doc.ClientID
	}
	if len(s.Scopes) == 0 {
		s.Scopes = doc.Scopes
	}
	return s, nil
}

func (s OAuthSettings) DeviceConfig(provider string) (DeviceCodeConfig, error) {
	if s.Flow != "" && s.Flow != FlowDevice {
		return DeviceCodeConfig{}, fmt.Errorf("%s: oauth flow %q is not the device flow", provider, s.Flow)
	}
	if strings.TrimSpace(s.ClientID) == "" {
		return DeviceCodeConfig{}, fmt.Errorf("%s: oauth names no client_id", provider)
	}
	oc, iu, err := discover(oidcHTTP, s.Issuer)
	if err != nil {
		return DeviceCodeConfig{}, fmt.Errorf("%s: oauth %w", provider, err)
	}
	if oc.DeviceAuthorizationEndpoint == "" {
		return DeviceCodeConfig{}, fmt.Errorf("%s: %s offers no device flow (no device_authorization_endpoint)", provider, s.Issuer)
	}
	if !sameOrigin(oc.DeviceAuthorizationEndpoint, iu) {
		return DeviceCodeConfig{}, fmt.Errorf("%s: device endpoint %q is not on the issuer %s", provider, oc.DeviceAuthorizationEndpoint, s.Issuer)
	}
	return DeviceCodeConfig{
		ProviderName: provider,
		ClientID:     strings.TrimSpace(s.ClientID),
		Scope:        withOfflineAccess(s.Scopes),
		DeviceURL:    oc.DeviceAuthorizationEndpoint,
		TokenURL:     oc.TokenEndpoint,
	}, nil
}

func withOfflineAccess(scopes []string) string {
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

func getJSON(endpoint string, out any) error { return getJSONWith(oidcHTTP, endpoint, out) }

func getJSONWith(client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
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

func LoginOIDCDevice(cfg DeviceCodeConfig, issuer string) (DeviceTokens, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim(cfg.ProviderName+" signs in with "+issuer+" (device flow)."))
	fmt.Fprintln(os.Stderr)
	tok, err := LoginDeviceCode(cfg, printDevicePrompt)
	if err != nil {
		return DeviceTokens{}, err
	}
	if tok.RefreshToken == "" {
		return DeviceTokens{}, fmt.Errorf("%s: %s issued no refresh token, so the login would end with this access token", cfg.ProviderName, issuer)
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized by "+issuer)
	return tok, nil
}
