package boot

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

const (
	SchemeOIDCDevice = "oidc-device"
	SchemeAPIKey     = "api-key"
)

type TroupeAuth struct {
	Scheme  string `toml:"scheme"`
	KeyFile string `toml:"key_file"`
	auth.OAuthSettings
}

type Troupe struct {
	Name     string      `toml:"-"`
	URL      string      `toml:"url"`
	Audience string      `toml:"audience"`
	Auth     *TroupeAuth `toml:"auth"`
	keyPath  string
}

func (t Troupe) APIKey() bool { return t.Auth != nil && t.Auth.Scheme == SchemeAPIKey }

func (t Troupe) StoredKeyPath() string { return t.keyPath }

var WorkloadSource auth.WorkloadTokens

func (t Troupe) Remote() bool {
	u, err := url.Parse(t.URL)
	return err == nil && u.Scheme == "wss"
}

func (t Troupe) HushCredential() string { return "troupe:" + t.Name }

func SelectedTroupeName(loaded *config.Loaded) string {
	if sel := strings.TrimSpace(os.Getenv("FIGARO_TROUPE")); sel != "" {
		return sel
	}
	if loaded != nil {
		return strings.TrimSpace(loaded.Config.CLI.Troupe)
	}
	return ""
}

func SelectedTroupe(loaded *config.Loaded) (Troupe, bool, error) {
	sel := SelectedTroupeName(loaded)
	if sel == "" {
		return Troupe{}, false, nil
	}
	if strings.Contains(sel, "://") {
		return checkTroupe(Troupe{URL: sel})
	}
	t, err := LoadTroupe(loaded, sel)
	if err != nil {
		return Troupe{}, false, err
	}
	return checkTroupe(t)
}

func TroupeNames(loaded *config.Loaded) ([]string, error) {
	entries, err := os.ReadDir(loaded.TroupesDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok && !e.IsDir() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

func LoadTroupe(loaded *config.Loaded, name string) (Troupe, error) {
	if name == "" || name != filepath.Base(name) {
		return Troupe{}, fmt.Errorf("troupe name %q is not a bare name", name)
	}
	path := filepath.Join(loaded.TroupesDir(), name+".toml")
	var t Troupe
	md, err := toml.DecodeFile(path, &t)
	if err != nil {
		if os.IsNotExist(err) {
			return Troupe{}, fmt.Errorf("no troupe %q (%s)", name, path)
		}
		return Troupe{}, fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Troupe{}, fmt.Errorf("%s: unknown key %s", path, strings.Join(keys, ", "))
	}
	if err := checkTroupeAuth(t); err != nil {
		return Troupe{}, fmt.Errorf("%s: %w", path, err)
	}
	t.Name = name
	t.keyPath = filepath.Join(loaded.TroupesDir(), name+".key")
	return t, nil
}

func checkTroupeAuth(t Troupe) error {
	if t.Auth == nil {
		return nil
	}
	switch t.Auth.Scheme {
	case SchemeOIDCDevice:
		if t.Auth.KeyFile != "" {
			return errors.New("auth.key_file belongs to scheme \"api-key\"")
		}
	case SchemeAPIKey:
		o := t.Auth.OAuthSettings
		if o.Issuer != "" || o.ClientID != "" || len(o.Scopes) > 0 || o.Flow != "" || o.Discovery != "" {
			return errors.New("scheme \"api-key\" takes key_file only; issuer, client_id, scopes, flow and discovery are oidc-device's")
		}
		if t.Auth.KeyFile != "" && !filepath.IsAbs(t.Auth.KeyFile) {
			return fmt.Errorf("auth.key_file %q: want an absolute path", t.Auth.KeyFile)
		}
		if !strings.HasPrefix(t.URL, "wss://") {
			return fmt.Errorf("scheme \"api-key\" sends the key over wss only, and url is %q", t.URL)
		}
	default:
		return fmt.Errorf("auth.scheme %q is not one of: %s, %s", t.Auth.Scheme, SchemeOIDCDevice, SchemeAPIKey)
	}
	return nil
}

func checkTroupe(t Troupe) (Troupe, bool, error) {
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme == "" {
		return Troupe{}, false, fmt.Errorf("troupe %q: url %q is not a URL", t.Name, t.URL)
	}
	switch u.Scheme {
	case "unix", "wss":
	default:
		return Troupe{}, false, fmt.Errorf("troupe %q: scheme %q is not one of unix, wss", t.Name, u.Scheme)
	}
	return t, true, nil
}

func TroupeResolver(t Troupe) auth.TokenResolver {
	if !t.Remote() {
		return nil
	}
	audience := t.Audience
	if audience == "" {
		audience = t.URL
	}
	if t.APIKey() {
		return &auth.Aggregate{Strategies: apiKeyStrategies(t)}
	}
	strategies := []auth.CredentialStrategy{&auth.Workload{Source: WorkloadSource, Audience: audience}}
	if t.Auth != nil {
		name := t.HushCredential()
		strategies = append(strategies, &hushStrategy{build: func(c *hush.Client) auth.CredentialStrategy {
			return &auth.OAuth{Hush: c, Name: name}
		}})
	}
	return &auth.Aggregate{Strategies: strategies}
}
