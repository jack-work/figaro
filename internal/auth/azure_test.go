package auth

import (
	"strings"
	"testing"
)

func TestAzureDefaultsAreTheAzureCLIClientAndCognitiveServices(t *testing.T) {
	cfg, err := AzureSettings{}.DeviceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != AzureCLIClientID {
		t.Fatalf("client_id = %q", cfg.ClientID)
	}
	if cfg.DeviceURL != "https://login.microsoftonline.com/organizations/oauth2/v2.0/devicecode" ||
		cfg.TokenURL != "https://login.microsoftonline.com/organizations/oauth2/v2.0/token" {
		t.Fatalf("urls = %s %s", cfg.DeviceURL, cfg.TokenURL)
	}
	if cfg.Scope != "https://cognitiveservices.azure.com/.default offline_access" {
		t.Fatalf("scope = %q", cfg.Scope)
	}
}

func TestAzureScopeAlwaysAsksForARefreshToken(t *testing.T) {
	// Without offline_access Entra issues no refresh token, and the login
	// would die with its first access token an hour later.
	for _, in := range []string{"https://ai.azure.com/.default", "https://ai.azure.com/.default offline_access", "offline_access https://ai.azure.com/.default"} {
		cfg, err := AzureSettings{Scope: in}.DeviceConfig()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(cfg.Scope, "offline_access") != 1 || !strings.Contains(cfg.Scope, "https://ai.azure.com/.default") {
			t.Fatalf("%q -> %q", in, cfg.Scope)
		}
	}
}

func TestAzureTenantAndAuthority(t *testing.T) {
	cfg, err := AzureSettings{Tenant: "contoso.onmicrosoft.com", Authority: "https://login.microsoftonline.us/"}.DeviceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenURL != "https://login.microsoftonline.us/contoso.onmicrosoft.com/oauth2/v2.0/token" {
		t.Fatalf("token url = %s", cfg.TokenURL)
	}
	if _, err := (AzureSettings{Tenant: "72f988bf-86f1-41af-91ab-2d7cd011db47"}).DeviceConfig(); err != nil {
		t.Fatalf("a tenant id was refused: %v", err)
	}
}

func TestAzureRefusesWhatHushWouldRefuseLater(t *testing.T) {
	// hush's entra grant pins the token endpoint to Microsoft's hosts. A
	// login that reached the device prompt and then failed at registration
	// would cost the reader a sign-in for nothing, so the same pin is checked
	// before the first request.
	bad := []AzureSettings{
		{Authority: "https://evil.example"},
		{Authority: "http://login.microsoftonline.com"},
		{Tenant: "../evil"},
		{Tenant: "contoso/oauth2"},
		{Tenant: "a b"},
	}
	for _, s := range bad {
		if _, err := s.DeviceConfig(); err == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}
