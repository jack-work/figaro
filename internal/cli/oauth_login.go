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
	in, err := s.SignIn(name)
	if err != nil {
		return err
	}
	ensureHush()
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
	fmt.Fprintln(os.Stderr, "  "+green("\u2713")+" hush holds the "+credential+" credential and refreshes it "+dim("(hush oauth list)"))
	fmt.Fprintln(os.Stderr, "  "+dim("next: "+next))
	return nil
}
