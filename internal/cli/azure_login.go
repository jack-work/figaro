package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/term"
)

const (
	azureProvider = "azure"
	entraGrant    = hush.GrantEntra
)

type azureFile struct {
	BaseURL string `toml:"base_url"`
	auth.AzureSettings
}

func runAzureLogin(loaded *config.Loaded) error {
	var f azureFile
	if _, err := toml.DecodeFile(loaded.ProviderAuthPath(azureProvider), &f); err != nil && !os.IsNotExist(err) {
		return err
	}
	base := strings.TrimSpace(os.Getenv("AZURE_BASE_URL"))
	if base == "" {
		base = f.BaseURL
	}
	if base == "" {
		line, err := term.ReadLine("       Endpoint (https://<resource>.openai.azure.com/openai/v1): ")
		if err != nil {
			return fmt.Errorf("read endpoint: %w", err)
		}
		base = line
	}
	base, err := azureEndpoint(base)
	if err != nil {
		return err
	}
	if f.Tenant == "" {
		line, err := term.ReadLine("       Tenant (domain or id, blank for any work account): ")
		if err != nil {
			return fmt.Errorf("read tenant: %w", err)
		}
		f.Tenant = strings.TrimSpace(line)
	}
	cfg, err := f.AzureSettings.DeviceConfig()
	if err != nil {
		return err
	}
	ensureHush()
	if err := MustHush().EnsureGrant(entraGrant); err != nil {
		return azureGrantError(err)
	}
	tok, err := auth.LoginAzure(cfg)
	if err != nil {
		return err
	}
	if err := MustHush().Client().OAuthRegister(azureRegistration(cfg, tok)); err != nil {
		return azureRegisterError(err)
	}
	if err := writeAzureSettings(loaded, base, f.AzureSettings); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the Entra credential and refreshes it "+dim("(hush oauth list)"))
	fmt.Fprintln(os.Stderr, "  "+dim("next: figaro set system.provider azure; figaro set system.model <deployment name>"))
	return nil
}

func azureRegistration(cfg auth.DeviceCodeConfig, tok auth.DeviceTokens) hush.OAuthRegisterRequest {
	return hush.OAuthRegisterRequest{
		Name:         azureProvider,
		TokenURL:     cfg.TokenURL,
		ClientID:     cfg.ClientID,
		Scopes:       cfg.Scope,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Grant:        entraGrant,
	}
}

func azureGrantError(err error) error {
	return fmt.Errorf("signing in to Azure needs a hush with the %s grant, which refreshes the login; update hush, restart its agent (hush down && hush up) and retry: %w", entraGrant, err)
}

func azureRegisterError(err error) error {
	if strings.Contains(err.Error(), `unknown grant "`+entraGrant+`"`) {
		return azureGrantError(err)
	}
	return fmt.Errorf("hand the Entra credential to hush: %w", err)
}

func azureEndpoint(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("azure: the endpoint must be an https URL, e.g. https://<resource>.openai.azure.com/openai/v1")
	}
	return base, nil
}

func writeAzureSettings(loaded *config.Loaded, base string, s auth.AzureSettings) error {
	return mergeProviderFile(loaded, azureProvider, map[string]string{
		"base_url":  base,
		"tenant":    s.Tenant,
		"client_id": s.ClientID,
		"scope":     s.Scope,
		"authority": s.Authority,
	})
}

func mergeProviderFile(loaded *config.Loaded, provider string, kv map[string]string) error {
	path := loaded.ProviderAuthPath(provider)
	doc := map[string]any{}
	if _, err := toml.DecodeFile(path, &doc); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s: %w", path, err)
	}
	for k, v := range kv {
		if v = strings.TrimSpace(v); v != "" {
			doc[k] = v
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := toml.NewEncoder(out).Encode(doc); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
