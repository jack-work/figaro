package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	hush "github.com/jack-work/hush/client"
)

type confidentialTOML struct {
	TokenURL         string `toml:"token_url"`
	ClientID         string `toml:"client_id"`
	ClientSecretFile string `toml:"client_secret_file"`
	Scopes           string `toml:"scopes,omitempty"`
	AccessToken      string `toml:"access_token"`
	RefreshToken     string `toml:"refresh_token"`
	RefreshTokenPrev string `toml:"refresh_token_prev,omitempty"`
	IssuedAt         int64  `toml:"issued_at,omitempty"`
	ExpiresAt        int64  `toml:"expires_at"`
}

type confidentialCache struct {
	mod              time.Time
	tokenURL         string
	clientID         string
	clientSecretFile string
	scopes           string
	access           string
	refresh          string
	prevRefresh      string
	issuedAt         time.Time
	expiresAt        time.Time
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func readClientSecret(rawPath string) (string, error) {
	p := expandHome(strings.TrimSpace(rawPath))
	if p == "" {
		return "", nil
	}
	if !filepath.IsAbs(p) {
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			p = filepath.Join(dir, p)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("client_secret_file %s: %w", p, err)
	}
	secret := strings.TrimSpace(string(b))
	clear(b)
	if secret == "" {
		return "", fmt.Errorf("client_secret_file %s is empty", p)
	}
	return secret, nil
}

func defaultOAuthStateDir() string {
	if d := os.Getenv("FIGARO_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "figaro")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "figaro")
}

func confidentialDir(stateDir string) string {
	if stateDir == "" {
		stateDir = defaultOAuthStateDir()
	}
	return filepath.Join(stateDir, "oauth")
}

func confidentialPath(stateDir, name string) string {
	return filepath.Join(confidentialDir(stateDir), name+".toml")
}

func RegisterOAuth(h *hush.Client, name string, in OIDCSignIn) error {
	return RegisterOAuthIn(h, "", name, in)
}

func RegisterOAuthIn(h *hush.Client, stateDir, name string, in OIDCSignIn) error {
	secretFile := strings.TrimSpace(in.ClientSecretFile)
	if secretFile == "" {
		_ = os.Remove(confidentialPath(stateDir, name))
		return h.OAuthRegister(hush.OAuthRegisterRequest{
			Name:         name,
			TokenURL:     in.TokenURL,
			ClientID:     in.ClientID,
			Scopes:       in.Scope,
			AccessToken:  in.Tokens.AccessToken,
			RefreshToken: in.Tokens.RefreshToken,
			ExpiresIn:    in.Tokens.ExpiresIn,
			Grant:        hush.GrantRefreshToken,
		})
	}
	_ = h.OAuthDelete(name)
	now := time.Now()
	ttl := time.Duration(in.Tokens.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	st := &confidentialCache{
		tokenURL:         in.TokenURL,
		clientID:         in.ClientID,
		clientSecretFile: secretFile,
		scopes:           in.Scope,
		access:           in.Tokens.AccessToken,
		refresh:          in.Tokens.RefreshToken,
		issuedAt:         now,
		expiresAt:        now.Add(ttl),
	}
	return saveConfidential(h, confidentialPath(stateDir, name), st)
}

func ConfidentialOAuthList() ([]string, error) {
	entries, err := os.ReadDir(confidentialDir(""))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok && !e.IsDir() && n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func ConfidentialOAuthRemove(name string) (bool, error) {
	err := os.Remove(confidentialPath("", name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func ConfidentialOAuthDelete(name string) error {
	_, err := ConfidentialOAuthRemove(name)
	return err
}

func saveConfidential(h *hush.Client, path string, st *confidentialCache) error {
	plain := map[string]string{"access": st.access, "refresh": st.refresh}
	if st.prevRefresh != "" {
		plain["prev"] = st.prevRefresh
	}
	sealed, err := h.Encrypt(plain)
	if err != nil {
		return fmt.Errorf("seal oauth tokens with hush: %w", err)
	}
	doc := confidentialTOML{
		TokenURL:         st.tokenURL,
		ClientID:         st.clientID,
		ClientSecretFile: st.clientSecretFile,
		Scopes:           st.scopes,
		AccessToken:      sealed["access"],
		RefreshToken:     sealed["refresh"],
		RefreshTokenPrev: sealed["prev"],
		IssuedAt:         st.issuedAt.UnixMilli(),
		ExpiresAt:        st.expiresAt.UnixMilli(),
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if info, err := os.Stat(path); err == nil {
		st.mod = info.ModTime()
	}
	return nil
}

func (o *OAuth) loadConfidentialLocked(path string, info fs.FileInfo) error {
	if o.conf != nil && o.conf.mod.Equal(info.ModTime()) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc confidentialTOML
	if err := toml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	vals := map[string]string{"access": doc.AccessToken, "refresh": doc.RefreshToken}
	if doc.RefreshTokenPrev != "" {
		vals["prev"] = doc.RefreshTokenPrev
	}
	dec, err := o.Hush.Decrypt(vals)
	if err != nil {
		return err
	}
	prevToken := ""
	if o.conf != nil {
		prevToken = o.conf.access
	}
	o.conf = &confidentialCache{
		mod:              info.ModTime(),
		tokenURL:         doc.TokenURL,
		clientID:         doc.ClientID,
		clientSecretFile: doc.ClientSecretFile,
		scopes:           doc.Scopes,
		access:           dec["access"],
		refresh:          dec["refresh"],
		prevRefresh:      dec["prev"],
		issuedAt:         time.UnixMilli(doc.IssuedAt),
		expiresAt:        time.UnixMilli(doc.ExpiresAt),
	}
	if d := o.dead.Load(); d != nil && o.conf.access != "" && o.conf.access != prevToken && o.conf.access != d.token {
		if o.dead.CompareAndSwap(d, nil) {
			slog.Info("provider credential has a new login", "credential", o.Name)
		}
	}
	return nil
}

func (o *OAuth) tryResolveConfidential() (string, bool, error) {
	path := confidentialPath(o.StateDir, o.Name)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", true, err
	}
	o.confMu.Lock()
	defer o.confMu.Unlock()
	if err := o.loadConfidentialLocked(path, info); err != nil {
		return "", true, err
	}
	if d := o.dead.Load(); d != nil && d.token == o.conf.access {
		return "", true, d.err
	}
	skew := tokenSkew
	if ttl := o.conf.expiresAt.Sub(o.conf.issuedAt); ttl > 0 && ttl/2 < skew {
		skew = ttl / 2
	}
	if o.conf.access != "" && time.Now().Before(o.conf.expiresAt.Add(-skew)) {
		return o.conf.access, true, nil
	}
	tok, err := o.refreshConfidentialLocked(path)
	return tok, true, err
}

func (o *OAuth) invalidateConfidential(token string) (bool, error) {
	path := confidentialPath(o.StateDir, o.Name)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	o.confMu.Lock()
	defer o.confMu.Unlock()
	if err := o.loadConfidentialLocked(path, info); err != nil {
		return true, err
	}
	if token != "" && o.conf.access != "" && o.conf.access != token {
		return true, nil
	}
	_, err = o.refreshConfidentialLocked(path)
	return true, err
}

func (o *OAuth) refreshConfidentialLocked(path string) (string, error) {
	secretFile := strings.TrimSpace(o.ClientSecretFile)
	if secretFile == "" {
		secretFile = o.conf.clientSecretFile
	}
	secret, err := readClientSecret(secretFile)
	if err != nil {
		return "", fmt.Errorf("oauth refresh for %q: %w", o.Name, err)
	}
	tr, permanent, err := mintRefreshWithSecret(context.Background(), oauthClient(), o.conf.tokenURL, o.conf.clientID, secret, o.conf.refresh)
	if err != nil && permanent && o.conf.prevRefresh != "" {
		if retryTR, retryPerm, retryErr := mintRefreshWithSecret(context.Background(), oauthClient(), o.conf.tokenURL, o.conf.clientID, secret, o.conf.prevRefresh); retryErr == nil {
			tr, permanent, err = retryTR, false, nil
		} else {
			permanent = retryPerm
		}
	}
	if err != nil {
		if permanent {
			dead := &deadGrant{token: o.conf.access, err: &LoginRequiredError{Credential: o.Name, Cause: err}}
			if o.dead.Swap(dead) == nil {
				slog.Warn("provider credential needs login; requests stop until it has one", "credential", o.Name, "run", "figaro provider login "+o.Name)
			}
			return "", dead.err
		}
		return "", fmt.Errorf("oauth refresh for %q: %w", o.Name, err)
	}
	now := time.Now()
	ttl := tr.lifetime()
	if ttl <= 0 {
		ttl = time.Hour
	}
	if tr.RefreshToken != "" && tr.RefreshToken != o.conf.refresh {
		o.conf.prevRefresh = o.conf.refresh
		o.conf.refresh = tr.RefreshToken
	}
	o.conf.access = tr.AccessToken
	o.conf.issuedAt = now
	o.conf.expiresAt = now.Add(ttl)
	if err := saveConfidential(o.Hush, path, o.conf); err != nil {
		return "", fmt.Errorf("oauth refresh for %q: %w", o.Name, err)
	}
	o.dead.Store(nil)
	return o.conf.access, nil
}

func mintRefreshWithSecret(ctx context.Context, client *http.Client, endpoint, clientID, secret, refreshToken string) (tokenResponse, bool, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, false, fmt.Errorf("client %s: token request: %w", clientID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, false, err
	}
	if resp.StatusCode != http.StatusOK {
		permanent := resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests
		return tokenResponse{}, permanent, fmt.Errorf("client %s: token endpoint: HTTP %d", clientID, resp.StatusCode)
	}
	var out struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return tokenResponse{}, false, fmt.Errorf("client %s: decode token response: %w", clientID, err)
	}
	if out.AccessToken == "" {
		return tokenResponse{}, true, fmt.Errorf("client %s: token endpoint returned no access_token", clientID)
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return tokenResponse{}, true, fmt.Errorf("client %s: token endpoint returned an unsupported token type", clientID)
	}
	return tokenResponse{IDToken: out.IDToken, AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresIn: out.ExpiresIn}, false, nil
}
