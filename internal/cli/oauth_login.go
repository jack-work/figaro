package cli

import (
	"context"
	"fmt"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

func runOAuthLogin(ctx context.Context, loaded *config.Loaded, name, credential string, s auth.OAuthSettings, next string, c auth.Console) error {
	s, err := s.Discovered()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	in, err := s.SignIn(ctx, name, c)
	if err != nil {
		return err
	}
	headers, err := s.LoginHeaders(in.Tokens.IDToken)
	if err != nil {
		return err
	}
	if err := auth.RegisterOAuth(MustHush().Client(), credential, in); err != nil {
		return fmt.Errorf("hand the %s credential to hush: %w", name, err)
	}
	if err := persistLoginHeaders(loaded, name, headers); err != nil {
		return err
	}
	c.Done("hush holds the " + credential + " credential and refreshes it (hush oauth list)")
	c.Note("next: " + next)
	return nil
}
