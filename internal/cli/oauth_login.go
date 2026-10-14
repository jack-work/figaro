package cli

import (
	"fmt"
	"os"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider/routed"
)

func init() {
	routed.DeclaredLogin = runOAuthLogin
}

func runOAuthLogin(_ *config.Loaded, name string, s auth.OAuthSettings) error {
	s, err := s.Discovered()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	cfg, err := s.DeviceConfig(name)
	if err != nil {
		return err
	}
	tok, err := auth.LoginOIDCDevice(cfg, s.Issuer)
	if err != nil {
		return err
	}
	ensureHush()
	if err := MustHush().Client().OAuthRegister(hush.OAuthRegisterRequest{
		Name:         name,
		TokenURL:     cfg.TokenURL,
		ClientID:     cfg.ClientID,
		Scopes:       cfg.Scope,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Grant:        hush.GrantRefreshToken,
	}); err != nil {
		return fmt.Errorf("hand the %s credential to hush: %w", name, err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the "+name+" credential and refreshes it "+dim("(hush oauth list)"))
	fmt.Fprintln(os.Stderr, "  "+dim("next: figaro set system.provider "+name+"; figaro set system.model <model>"))
	return nil
}
