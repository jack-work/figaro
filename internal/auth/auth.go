// Package auth manages OAuth tokens with encrypted storage via hush.
//
// Tokens (access + refresh) are encrypted at rest using the hush agent.
// The hush agent must be running for figaro to start. This package
// never touches raw key material on disk.
//
// The design is provider-agnostic via OAuthConfig, so it can be
// extracted into hush as a managed-token-refresh feature later.
package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	hush "github.com/jack-work/hush/client"
)

// OAuthConfig describes an OAuth provider's endpoints and parameters.
// Provider-agnostic: pass different configs for Anthropic, GitHub, etc.
type OAuthConfig struct {
	ProviderName string // key in auth file, e.g. "anthropic"
	AuthorizeURL string
	TokenURL     string
	RedirectURI  string
	ClientID     string
	Scopes       string
}

// StoredCredentials is the on-disk format for a provider's tokens.
type StoredCredentials struct {
	AccessToken  string `json:"access_token"`  // AGE-ENC[...] or plaintext
	RefreshToken string `json:"refresh_token"` // AGE-ENC[...]
	ExpiresAt    int64  `json:"expires_at"`    // unix millis, plaintext
}

// AuthFile is the full on-disk auth file (all providers).
type AuthFile map[string]*StoredCredentials

// TokenManager handles encrypted storage and auto-refresh of OAuth tokens.
type TokenManager struct {
	hush     *hush.Client
	config   OAuthConfig
	filePath string

	// In-memory cache (plaintext, never written to disk)
	cached    string
	expiresAt int64
}

// NewManager creates a TokenManager for the given provider config.
// filePath is the path to the auth file (e.g. ~/.config/figaro/auth.json).
func NewManager(hushClient *hush.Client, config OAuthConfig, filePath string) *TokenManager {
	return &TokenManager{
		hush:     hushClient,
		config:   config,
		filePath: filePath,
	}
}

// DefaultAuthFilePath returns ~/.config/figaro/auth.json, respecting XDG.
func DefaultAuthFilePath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "figaro", "auth.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "figaro", "auth.json")
}

// AccessToken returns a valid access token, refreshing if expired.
// Requires the hush agent to be running.
func (m *TokenManager) AccessToken() (string, error) {
	// 1. Check in-memory cache
	if m.cached != "" && time.Now().UnixMilli() < m.expiresAt {
		return m.cached, nil
	}

	// 2. Ping hush agent
	if err := m.hush.Ping(); err != nil {
		return "", fmt.Errorf("hush agent is not running. Start it: hush up -d")
	}

	// 3. Read auth file
	creds, err := m.readCredentials()
	if err != nil {
		return "", fmt.Errorf("not logged in. Run: figaro login")
	}

	// 4. If access token not expired, decrypt and return it
	if time.Now().UnixMilli() < creds.ExpiresAt-5*60*1000 {
		token, err := m.decrypt(creds.AccessToken)
		if err != nil {
			return "", fmt.Errorf("decrypt access token: %w", err)
		}
		m.cached = token
		m.expiresAt = creds.ExpiresAt
		return token, nil
	}

	// 5. Expired — decrypt refresh token and refresh
	refreshToken, err := m.decrypt(creds.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt refresh token: %w", err)
	}

	newAccess, newRefresh, expiresIn, err := m.refreshTokens(refreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh failed. Run: figaro login\n  detail: %w", err)
	}

	// 6. Encrypt and persist new tokens
	if err := m.saveTokens(newAccess, newRefresh, expiresIn); err != nil {
		return "", fmt.Errorf("save refreshed tokens: %w", err)
	}

	return newAccess, nil
}

// SaveLogin encrypts and persists tokens from a fresh login.
func (m *TokenManager) SaveLogin(accessToken, refreshToken string, expiresIn int) error {
	return m.saveTokens(accessToken, refreshToken, expiresIn)
}

// --- internal helpers ---

func (m *TokenManager) readCredentials() (*StoredCredentials, error) {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return nil, err
	}
	var authFile AuthFile
	if err := json.Unmarshal(data, &authFile); err != nil {
		return nil, err
	}
	creds, ok := authFile[m.config.ProviderName]
	if !ok {
		return nil, fmt.Errorf("no credentials for %s", m.config.ProviderName)
	}
	return creds, nil
}

func (m *TokenManager) saveTokens(accessToken, refreshToken string, expiresIn int) error {
	// Encrypt both tokens
	encAccess, err := m.encrypt(accessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err := m.encrypt(refreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	expiresAt := time.Now().UnixMilli() + int64(expiresIn)*1000 - 5*60*1000

	// Read existing auth file (or create new)
	var authFile AuthFile
	if data, err := os.ReadFile(m.filePath); err == nil {
		json.Unmarshal(data, &authFile)
	}
	if authFile == nil {
		authFile = make(AuthFile)
	}

	authFile[m.config.ProviderName] = &StoredCredentials{
		AccessToken:  encAccess,
		RefreshToken: encRefresh,
		ExpiresAt:    expiresAt,
	}

	data, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	if err := os.WriteFile(m.filePath, data, 0600); err != nil {
		return err
	}

	// Cache in memory
	m.cached = accessToken
	m.expiresAt = expiresAt

	return nil
}

func (m *TokenManager) encrypt(plaintext string) (string, error) {
	result, err := m.hush.Encrypt(map[string]string{"v": plaintext})
	if err != nil {
		return "", err
	}
	return result["v"], nil
}

func (m *TokenManager) decrypt(ciphertext string) (string, error) {
	result, err := m.hush.Decrypt(map[string]string{"v": ciphertext})
	if err != nil {
		return "", err
	}
	return result["v"], nil
}

func (m *TokenManager) refreshTokens(refreshToken string) (access, refresh string, expiresIn int, err error) {
	body := fmt.Sprintf(`{"grant_type":"refresh_token","client_id":"%s","refresh_token":"%s"}`,
		m.config.ClientID, refreshToken)

	resp, err := http.Post(m.config.TokenURL, "application/json", strings.NewReader(body))
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody json.RawMessage
		json.NewDecoder(resp.Body).Decode(&errBody)
		return "", "", 0, fmt.Errorf("token refresh %d: %s", resp.StatusCode, string(errBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", 0, err
	}

	return tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, nil
}
