package cli

import "github.com/jack-work/figaro/internal/boot"

type oauthStore interface {
	OAuthList() ([]string, error)
	OAuthDelete(name string) error
}

type noOAuth struct{}

func (noOAuth) OAuthList() ([]string, error) { return nil, nil }
func (noOAuth) OAuthDelete(string) error     { return nil }

func cliOAuthStore(interactive bool) (oauthStore, error) {
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
	return h.Client(), nil
}
