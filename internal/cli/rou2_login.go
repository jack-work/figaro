package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/term"
)

const rou2Provider = "rou2"

func runRou2Login(loaded *config.Loaded) error {
	var f struct {
		BaseURL string `toml:"base_url"`
	}
	if _, err := toml.DecodeFile(loaded.ProviderAuthPath(rou2Provider), &f); err != nil && !os.IsNotExist(err) {
		return err
	}
	base := strings.TrimSpace(os.Getenv(providerPkg.BaseURLEnv(rou2Provider)))
	if base == "" {
		base = f.BaseURL
	}
	if base == "" {
		line, err := term.ReadLine("       rou2 address (http://<host>:<port>): ")
		if err != nil {
			return fmt.Errorf("read address: %w", err)
		}
		base = line
	}
	root, err := auth.Rou2Root(base)
	if err != nil {
		return err
	}
	d, cfg, err := auth.DiscoverRou2(root)
	if err != nil {
		return err
	}
	tok, err := auth.LoginRou2(cfg, d.Issuer)
	if err != nil {
		return err
	}
	ensureHush()
	if err := MustHush().Client().OAuthRegister(hush.OAuthRegisterRequest{
		Name:         rou2Provider,
		TokenURL:     cfg.TokenURL,
		ClientID:     cfg.ClientID,
		Scopes:       cfg.Scope,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Grant:        hush.GrantRefreshToken,
	}); err != nil {
		return fmt.Errorf("hand the rou2 credential to hush: %w", err)
	}
	if err := mergeProviderFile(loaded, rou2Provider, map[string]string{"base_url": root + "/v1"}); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the rou2 credential and refreshes it "+dim("(hush oauth list)"))
	fmt.Fprintln(os.Stderr, "  "+dim("next: figaro set system.provider rou2; figaro set system.model "+rou2ModelHint(d)))
	return nil
}

func rou2ModelHint(d auth.Rou2Discovery) string {
	for _, u := range d.Upstreams {
		if u.Default {
			return u.Name + "/<model>"
		}
	}
	return "<upstream>/<model>"
}
