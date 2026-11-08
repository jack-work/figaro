package cli

import (
	"context"
	"fmt"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
)

func runOAuthLogin(ctx context.Context, name, credential string, s auth.OAuthSettings, next string, c auth.Console) error {
	s, err := s.Discovered()
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	in, err := s.SignIn(ctx, name, c)
	if err != nil {
		return err
	}
	if err := MustHush().Client().OAuthRegister(hush.OAuthRegisterRequest{
		Name:         credential,
		TokenURL:     in.TokenURL,
		ClientID:     in.ClientID,
		Scopes:       in.Scope,
		AccessToken:  in.Tokens.AccessToken,
		RefreshToken: in.Tokens.RefreshToken,
		ExpiresIn:    in.Tokens.ExpiresIn,
		Grant:        hush.GrantRefreshToken,
	}); err != nil {
		return fmt.Errorf("hand the %s credential to hush: %w", name, err)
	}
	c.Done("hush holds the " + credential + " credential and refreshes it (hush oauth list)")
	c.Note("next: " + next)
	return nil
}
