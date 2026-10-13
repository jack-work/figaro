package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

// The registration is the whole contract with hush: an entra grant, the
// token endpoint the device flow used, and the scope every refresh must name.
// A field missing here is a login that works for an hour.
func TestAzureRegistrationCarriesWhatARefreshNeeds(t *testing.T) {
	cfg, err := auth.AzureSettings{Tenant: "contoso.onmicrosoft.com"}.DeviceConfig()
	if err != nil {
		t.Fatal(err)
	}
	req := azureRegistration(cfg, auth.DeviceTokens{AccessToken: "A", RefreshToken: "R", ExpiresIn: 4532})
	want := hush.OAuthRegisterRequest{
		Name:         "azure",
		TokenURL:     "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token",
		ClientID:     auth.AzureCLIClientID,
		Scopes:       "https://cognitiveservices.azure.com/.default offline_access",
		AccessToken:  "A",
		RefreshToken: "R",
		ExpiresIn:    4532,
		Grant:        "entra",
	}
	if req != want {
		t.Fatalf("registration =\n%+v\nwant\n%+v", req, want)
	}
}

func TestAHushWithoutTheGrantIsNamedBeforeTheSignIn(t *testing.T) {
	// EnsureGrant's two refusals, verbatim from hush v0.12.0's managed
	// package: an external agent that predates the grant, and an embedded one
	// that still lacks it after the restart. Either way the fix is a newer
	// hush, and a restart alone does not supply a grant the binary lacks.
	for _, raw := range []string{
		`the running hush agent does not support the "entra" grant; it predates this build. Restart it and retry: hush down && hush up`,
		`the hush agent still does not support the "entra" grant after a restart`,
	} {
		err := azureGrantError(errors.New(raw))
		if !strings.Contains(err.Error(), "update hush") || !strings.Contains(err.Error(), raw) {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestAnOldHushIsNamedAsTheProblem(t *testing.T) {
	old := errors.New(`register oauth: oauth: refresh failed permanently: unknown grant "entra" (have [copilot refresh_token])`)
	err := azureRegisterError(old)
	if !strings.Contains(err.Error(), "update hush") {
		t.Fatalf("err = %v", err)
	}
	other := errors.New("hush agent is not running")
	if azureRegisterError(other).Error() == other.Error() {
		t.Fatal("an unrelated failure lost its context")
	}
	if strings.Contains(azureRegisterError(other).Error(), "update hush") {
		t.Fatal("an unrelated failure was blamed on the hush version")
	}
}

func TestAzureSettingsAreMergedIntoTheProviderFile(t *testing.T) {
	// The file may already hold an encrypted api_key or routing rules; the
	// login adds what it learned and leaves the rest alone.
	dir := t.TempDir()
	loaded := &config.Loaded{ConfigDir: dir}
	path := loaded.ProviderAuthPath("azure")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("api_key = \"AGE-ENC[x]\"\n[routing]\ndefault = \"responses\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAzureSettings(loaded, "https://r.openai.azure.com/openai/v1", auth.AzureSettings{Tenant: "contoso.onmicrosoft.com"}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(path, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["api_key"] != "AGE-ENC[x]" {
		t.Fatalf("api_key lost: %v", doc)
	}
	if r, _ := doc["routing"].(map[string]any); r["default"] != "responses" {
		t.Fatalf("routing lost: %v", doc)
	}
	if doc["base_url"] != "https://r.openai.azure.com/openai/v1" || doc["tenant"] != "contoso.onmicrosoft.com" {
		t.Fatalf("settings not written: %v", doc)
	}
	if _, ok := doc["client_id"]; ok {
		t.Fatalf("an unset field was written as empty: %v", doc)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestAzureEndpointIsChecked(t *testing.T) {
	for _, ok := range []string{"https://r.openai.azure.com/openai/v1", "https://r.services.ai.azure.com/openai/v1/"} {
		if _, err := azureEndpoint(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "r.openai.azure.com", "http://r.openai.azure.com/openai/v1", "https://"} {
		if _, err := azureEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
