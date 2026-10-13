package auth

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/jack-work/figaro/internal/term"
)

const (
	AzureCLIClientID  = "04b07795-8ddb-461a-bbee-02f9e1bf7b46"
	AzureDefaultScope = "https://cognitiveservices.azure.com/.default"
	azureAuthority    = "https://login.microsoftonline.com"
	azureTenant       = "organizations"
	offlineAccess     = "offline_access"
)

var azureAuthorities = map[string]bool{
	"login.microsoftonline.com":        true,
	"login.microsoftonline.us":         true,
	"login.partner.microsoftonline.cn": true,
}

var azureTenantName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

type AzureSettings struct {
	Tenant    string `toml:"tenant,omitempty"`
	ClientID  string `toml:"client_id,omitempty"`
	Scope     string `toml:"scope,omitempty"`
	Authority string `toml:"authority,omitempty"`
}

func (s AzureSettings) DeviceConfig() (DeviceCodeConfig, error) {
	authority := strings.TrimRight(strings.TrimSpace(s.Authority), "/")
	if authority == "" {
		authority = azureAuthority
	}
	u, err := url.Parse(authority)
	if err != nil || u.Scheme != "https" || !azureAuthorities[u.Host] || (u.Path != "" && u.Path != "/") {
		return DeviceCodeConfig{}, fmt.Errorf("azure: authority %q is not a Microsoft identity platform host (https://login.microsoftonline.com)", s.Authority)
	}
	tenant := strings.TrimSpace(s.Tenant)
	if tenant == "" {
		tenant = azureTenant
	}
	if !azureTenantName.MatchString(tenant) {
		return DeviceCodeConfig{}, fmt.Errorf("azure: tenant %q is neither a domain nor a tenant id", s.Tenant)
	}
	clientID := strings.TrimSpace(s.ClientID)
	if clientID == "" {
		clientID = AzureCLIClientID
	}
	base := authority + "/" + tenant + "/oauth2/v2.0"
	return DeviceCodeConfig{
		ProviderName: "azure",
		ClientID:     clientID,
		Scope:        azureScope(s.Scope),
		DeviceURL:    base + "/devicecode",
		TokenURL:     base + "/token",
	}, nil
}

func azureScope(scope string) string {
	var parts []string
	for _, p := range strings.Fields(scope) {
		if p != offlineAccess {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		parts = []string{AzureDefaultScope}
	}
	return strings.Join(append(parts, offlineAccess), " ")
}

func LoginAzure(cfg DeviceCodeConfig) (DeviceTokens, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("Azure signs in with the Microsoft Entra device flow."))
	fmt.Fprintln(os.Stderr)
	tok, err := LoginDeviceCode(cfg, printDevicePrompt)
	if err != nil {
		return DeviceTokens{}, err
	}
	if tok.RefreshToken == "" {
		return DeviceTokens{}, fmt.Errorf("azure: Entra issued no refresh token, so the login would end with this access token; the scope must include offline_access")
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized with Microsoft Entra")
	return tok, nil
}
