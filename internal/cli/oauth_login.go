package cli

import (
	"fmt"
	"os"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
)

func runOAuthLogin(name, credential string, s auth.OAuthSettings, next string) error {
	s, err := s.Discovered()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var (
		tok                     auth.DeviceTokens
		tokenURL, clientID, scp string
	)
	switch s.Flow {
	case auth.FlowPKCE:
		cfg, err := s.PKCEConfig(name)
		if err != nil {
			return err
		}
		if tok, err = auth.LoginPKCE(cfg); err != nil {
			return err
		}
		tokenURL, clientID, scp = cfg.TokenURL, cfg.ClientID, cfg.Scope
	case "", auth.FlowDevice:
		cfg, err := s.DeviceConfig(name)
		if err != nil {
			return err
		}
		if tok, err = auth.LoginOIDCDevice(cfg, s.Issuer); err != nil {
			return err
		}
		tokenURL, clientID, scp = cfg.TokenURL, cfg.ClientID, cfg.Scope
	default:
		return fmt.Errorf("%s: oauth flow %q is not one of %s, %s", name, s.Flow, auth.FlowDevice, auth.FlowPKCE)
	}
	ensureHush()
	if err := MustHush().Client().OAuthRegister(hush.OAuthRegisterRequest{
		Name:         credential,
		TokenURL:     tokenURL,
		ClientID:     clientID,
		Scopes:       scp,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Grant:        hush.GrantRefreshToken,
	}); err != nil {
		return fmt.Errorf("hand the %s credential to hush: %w", name, err)
	}
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the "+credential+" credential and refreshes it "+dim("(hush oauth list)"))
	fmt.Fprintln(os.Stderr, "  "+dim("next: "+next))
	return nil
}
