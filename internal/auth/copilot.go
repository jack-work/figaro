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
	"time"

	"github.com/jack-work/figaro/internal/term"
)

const (
	copilotClientID = "Iv1.b507a08c87ecfe98"
)

// CopilotOAuth holds the configuration for the GitHub Copilot device code flow.
var CopilotOAuth = DeviceCodeConfig{
	ProviderName: "copilot",
	ClientID:     copilotClientID,
	Scope:        "read:user",
}

type DeviceCodeConfig struct {
	ProviderName string
	ClientID     string
	Scope        string
}

// LoginDeviceCode runs the GitHub OAuth device code flow: it requests a
// user code, opens the browser, polls for approval, and returns the
// long-lived GitHub access token (which is then stored as the "refresh"
// credential for the Copilot token exchange).
func LoginDeviceCode(cfg DeviceCodeConfig, domain string, promptCode func(userCode, verificationURI string) error) (githubToken string, err error) {
	if domain == "" {
		domain = "github.com"
	}
	deviceCodeURL := fmt.Sprintf("https://%s/login/device/code", domain)
	accessTokenURL := fmt.Sprintf("https://%s/login/oauth/access_token", domain)

	// Step 1: request device + user code
	form := url.Values{
		"client_id": {cfg.ClientID},
		"scope":     {cfg.Scope},
	}
	req, err := http.NewRequest("POST", deviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("device code request failed (%d)", resp.StatusCode)
	}
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return "", fmt.Errorf("parse device code: %w", err)
	}

	// Step 2: show user the code and open browser
	if err := promptCode(device.UserCode, device.VerificationURI); err != nil {
		return "", err
	}
	openBrowserDevice(device.VerificationURI)

	// Step 3: poll for the access token
	interval := time.Duration(device.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)

	for {
		time.Sleep(interval)
		if time.Now().After(deadline) {
			return "", fmt.Errorf("device code expired (user did not authorize in time)")
		}
		token, done, newInterval, err := pollAccessToken(accessTokenURL, cfg.ClientID, device.DeviceCode)
		if err != nil {
			return "", err
		}
		if done {
			return token, nil
		}
		if newInterval > 0 {
			interval = time.Duration(newInterval) * time.Second
		}
	}
}

func pollAccessToken(tokenURL, clientID, deviceCode string) (token string, done bool, newInterval int, err error) {
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, 0, fmt.Errorf("poll token: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Interval    int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", false, 0, fmt.Errorf("parse poll response: %w", err)
	}
	if raw.AccessToken != "" {
		return raw.AccessToken, true, 0, nil
	}
	switch raw.Error {
	case "authorization_pending":
		return "", false, 0, nil
	case "slow_down":
		return "", false, raw.Interval, nil
	default:
		return "", false, 0, fmt.Errorf("device flow error: %s", raw.Error)
	}
}

// LoginCopilot runs the full Copilot login: device code flow for GitHub,
// then prints a success message. Returns the GitHub access token.
func LoginCopilot(domain string) (string, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("GitHub Copilot uses the device code flow."))
	fmt.Fprintln(os.Stderr)

	token, err := LoginDeviceCode(CopilotOAuth, domain, func(userCode, verificationURI string) error {
		fmt.Fprintln(os.Stderr, "       "+term.Dim("Open this URL and enter the code:"))
		fmt.Fprintln(os.Stderr, "       "+term.Cyan(verificationURI))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "       Code: "+term.Cyan(userCode))
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "       "+term.Dim("Waiting for authorization..."))
		return nil
	})
	if err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized with GitHub")
	return token, nil
}

func openBrowserDevice(url string) {
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
