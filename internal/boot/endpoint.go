package boot

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

type Endpoint struct {
	Name     string              `toml:"-"`
	URL      string              `toml:"url"`
	Audience string              `toml:"audience"`
	OAuth    *auth.OAuthSettings `toml:"oauth"`
}

var WorkloadSource auth.WorkloadTokens

func (e Endpoint) Remote() bool {
	u, err := url.Parse(e.URL)
	return err == nil && u.Scheme == "wss"
}

func (e Endpoint) HushCredential() string { return "endpoint:" + e.Name }

func SelectedEndpoint(loaded *config.Loaded) (Endpoint, bool, error) {
	sel := strings.TrimSpace(os.Getenv("FIGARO_ENDPOINT"))
	if sel == "" && loaded != nil {
		sel = strings.TrimSpace(loaded.Config.CLI.Endpoint)
	}
	if sel == "" {
		return Endpoint{}, false, nil
	}
	if strings.Contains(sel, "://") {
		return checkEndpoint(Endpoint{URL: sel})
	}
	e, err := LoadEndpoint(loaded, sel)
	if err != nil {
		return Endpoint{}, false, err
	}
	return checkEndpoint(e)
}

func LoadEndpoint(loaded *config.Loaded, name string) (Endpoint, error) {
	if name == "" || name != filepath.Base(name) {
		return Endpoint{}, fmt.Errorf("endpoint name %q is not a bare name", name)
	}
	path := filepath.Join(loaded.EndpointsDir(), name+".toml")
	var e Endpoint
	if _, err := toml.DecodeFile(path, &e); err != nil {
		if os.IsNotExist(err) {
			return Endpoint{}, fmt.Errorf("no endpoint %q (%s)", name, path)
		}
		return Endpoint{}, fmt.Errorf("%s: %w", path, err)
	}
	e.Name = name
	return e, nil
}

func checkEndpoint(e Endpoint) (Endpoint, bool, error) {
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme == "" {
		return Endpoint{}, false, fmt.Errorf("endpoint %q: url %q is not a URL", e.Name, e.URL)
	}
	switch u.Scheme {
	case "unix", "wss":
	default:
		return Endpoint{}, false, fmt.Errorf("endpoint %q: scheme %q is not one of unix, wss", e.Name, u.Scheme)
	}
	if e.OAuth != nil && e.Name == "" {
		return Endpoint{}, false, fmt.Errorf("an endpoint given as a URL cannot sign in; name it in endpoints/<name>.toml")
	}
	return e, true, nil
}

func EndpointResolver(e Endpoint) auth.TokenResolver {
	if !e.Remote() {
		return nil
	}
	audience := e.Audience
	if audience == "" {
		audience = e.URL
	}
	strategies := []auth.CredentialStrategy{&auth.Workload{Source: WorkloadSource, Audience: audience}}
	if e.OAuth != nil {
		name := e.HushCredential()
		strategies = append(strategies, &hushStrategy{build: func(c *hush.Client) auth.CredentialStrategy {
			return &auth.OAuth{Hush: c, Name: name}
		}})
	}
	return &auth.Aggregate{Strategies: strategies}
}
