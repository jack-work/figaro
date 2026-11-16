package cli

import (
	"errors"
	"slices"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/boot"
)

type oauthStore interface {
	OAuthList() ([]string, error)
	OAuthDelete(name string) error
}

type noOAuth struct{}

func (noOAuth) OAuthList() ([]string, error) { return nil, nil }
func (noOAuth) OAuthDelete(string) error     { return nil }

type hushWithConfidential struct {
	h *hush.Client
}

func (s hushWithConfidential) OAuthList() ([]string, error) {
	names, err := s.h.OAuthList()
	if err != nil {
		return nil, err
	}
	conf, err := auth.ConfidentialOAuthList()
	if err != nil {
		return nil, err
	}
	for _, n := range conf {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names, nil
}

func (s hushWithConfidential) OAuthDelete(name string) error {
	removedConf, err := auth.ConfidentialOAuthRemove(name)
	if err != nil {
		return err
	}
	if err := s.h.OAuthDelete(name); err != nil && (!removedConf || !errors.Is(err, hush.ErrOAuthNotFound)) {
		return err
	}
	return nil
}

func cliOAuthStore(interactive bool) (oauthStore, error) {
	refuseOffTape("the vault")
	h, err := boot.Hush()
	if err != nil {
		return nil, err
	}
	if !h.HasIdentity() {
		return noOAuth{}, nil
	}
	if interactive {
		ensureHush()
	} else if err := h.EnsureReady(); err != nil {
		return nil, err
	}
	return hushWithConfidential{h: h.Client()}, nil
}
