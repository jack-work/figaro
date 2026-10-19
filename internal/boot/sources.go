package boot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

type CredentialSource struct {
	Kind  string `json:"kind"`
	Where string `json:"where"`
	Owned bool   `json:"owned"`
}

const (
	SourceEnv       = "env"
	SourceKeyFile   = "api_key_file"
	SourceCredDir   = "credentials_directory"
	SourcePlainKey  = "api_key"
	SourceSealedKey = "sealed_api_key"
	SourceOAuth     = "oauth"
	SourceService   = "service_token"
)

func exchanges(loaded *config.Loaded, reg *providerPkg.Registration) bool {
	return reg != nil && reg.ExchangeGrant != "" && reg.ExchangeURL != nil && reg.ExchangeURL(loaded) != ""
}

func ConsultsHushOAuth(loaded *config.Loaded, reg *providerPkg.Registration) bool {
	return reg != nil && (reg.HasOAuth || exchanges(loaded, reg))
}

func CredentialSources(loaded *config.Loaded, reg *providerPkg.Registration, oauthNames []string) ([]CredentialSource, error) {
	var out []CredentialSource
	name, err := ServiceTokenOf(loaded, reg.Name)
	if err != nil {
		return nil, err
	}
	if name != "" {
		out = append(out, CredentialSource{Kind: SourceService, Where: serviceTokenWhere(loaded, name)})
	}
	for _, env := range reg.CredentialEnvs() {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			out = append(out, CredentialSource{Kind: SourceEnv, Where: "$" + env})
		}
	}
	var pa config.ProviderAuth
	if err := loaded.LoadProviderAuth(reg.Name, &pa); err != nil {
		return nil, err
	}
	path := loaded.ProviderAuthPath(reg.Name)
	if pa.APIKeyFile != "" {
		out = append(out, CredentialSource{Kind: SourceKeyFile, Where: keyFilePath(pa.APIKeyFile)})
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		p := filepath.Join(dir, reg.Name)
		if _, err := os.Stat(p); err == nil {
			out = append(out, CredentialSource{Kind: SourceCredDir, Where: p})
		}
	}
	switch {
	case strings.HasPrefix(pa.APIKey, "AGE-ENC["):
		out = append(out, CredentialSource{Kind: SourceSealedKey, Where: path, Owned: true})
	case pa.APIKey != "":
		out = append(out, CredentialSource{Kind: SourcePlainKey, Where: path})
	}
	if !ConsultsHushOAuth(loaded, reg) || !slices.Contains(oauthNames, reg.Name) {
		return out, nil
	}
	oauth := CredentialSource{Kind: SourceOAuth, Where: "hush credential " + reg.Name, Owned: true}
	if exchanges(loaded, reg) {
		return append([]CredentialSource{oauth}, out...), nil
	}
	return append(out, oauth), nil
}

func keyFilePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		return filepath.Join(dir, p)
	}
	return p + " (relative, and $CREDENTIALS_DIRECTORY is unset)"
}

func serviceTokenWhere(loaded *config.Loaded, name string) string {
	where := "service token " + name
	if _, err := ServiceToken(loaded, name); err != nil {
		return where + " (" + err.Error() + ")"
	}
	st := loaded.Config.ServiceTokens[name]
	from := st.TokenURL
	if from == "" {
		from = st.Issuer
	}
	where += ": client " + st.ClientID + " at " + from
	secret, _ := CredentialPath(st.SecretCredential)
	if _, err := os.Stat(secret); err != nil {
		where += " (secret " + secret + " missing)"
	}
	return where
}
