package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jack-work/figaro/internal/term"
	hush "github.com/jack-work/hush/client"
)

// Login runs the OAuth PKCE flow against the provider, exchanges the auth
// code for tokens, and registers the result with the hush agent. The
// agent owns refresh from that point on.
func Login(hushClient *hush.Client, cfg OAuthConfig, promptCode func() (string, error)) error {
	if err := hushClient.Ping(); err != nil {
		return fmt.Errorf("hush agent is not running. Start it: hush up -d")
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	params := url.Values{
		"code":                  {"true"},
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {cfg.RedirectURI},
		"scope":                 {cfg.Scopes},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {pkce.Verifier},
	}
	authURL := cfg.AuthorizeURL + "?" + params.Encode()

	// All presentation goes to stderr, indented + dimmed to match the
	// first-run TUI; the promptCode callback only reads (it must not print
	// its own prompt, or the prompt doubles up).
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("Opening your browser to sign in. If it doesn't open, visit:"))
	fmt.Fprintln(os.Stderr, "       "+term.Cyan(authURL))
	fmt.Fprintln(os.Stderr)
	openBrowser(authURL)

	fmt.Fprint(os.Stderr, "       Paste the code here: ")
	codeInput, err := promptCode()
	if err != nil {
		return fmt.Errorf("read code: %w", err)
	}
	codeInput = strings.TrimSpace(codeInput)

	parts := strings.SplitN(codeInput, "#", 2)
	code := parts[0]
	state := ""
	if len(parts) > 1 {
		state = parts[1]
	}

	tokenBody, _ := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     cfg.ClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  cfg.RedirectURI,
		"code_verifier": pkce.Verifier,
	})

	resp, err := http.Post(cfg.TokenURL, "application/json", strings.NewReader(string(tokenBody)))
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody json.RawMessage
		json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(errBody))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("parse token response: %w", err)
	}

	if err := hushClient.OAuthRegister(hush.OAuthRegisterRequest{
		Name:         cfg.ProviderName,
		AuthorizeURL: cfg.AuthorizeURL,
		TokenURL:     cfg.TokenURL,
		RedirectURI:  cfg.RedirectURI,
		ClientID:     cfg.ClientID,
		Scopes:       cfg.Scopes,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresIn:    tokenResp.ExpiresIn,
	}); err != nil {
		return fmt.Errorf("register with hush: %w", err)
	}

	fmt.Fprintln(os.Stderr, "       "+term.Green("✓")+" Logged in"+term.Dim(fmt.Sprintf(" · token expires in %ds", tokenResp.ExpiresIn)))
	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
