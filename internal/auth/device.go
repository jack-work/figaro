package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var UserAgent = "figaro"

type DeviceCodeConfig struct {
	ProviderName string
	ClientID     string
	Scope        string
	DeviceURL    string
	TokenURL     string
	Headers      map[string]string
}

type DeviceTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

type DevicePrompt struct {
	UserCode        string
	VerificationURI string
	Prefilled       bool
}

func LoginDeviceCode(cfg DeviceCodeConfig, prompt func(DevicePrompt) error) (DeviceTokens, error) {
	form := url.Values{"client_id": {cfg.ClientID}}
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}
	resp, err := postDeviceForm(cfg, cfg.DeviceURL, form)
	if err != nil {
		return DeviceTokens{}, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return DeviceTokens{}, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return DeviceTokens{}, fmt.Errorf("parse device code: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" {
		return DeviceTokens{}, fmt.Errorf("device code response is missing device_code or user_code")
	}

	p := DevicePrompt{UserCode: device.UserCode, VerificationURI: device.VerificationURI}
	if device.VerificationURIComplete != "" {
		p.VerificationURI = device.VerificationURIComplete
		p.Prefilled = true
	}
	if err := prompt(p); err != nil {
		return DeviceTokens{}, err
	}
	openBrowserDevice(p.VerificationURI)

	interval := max(time.Duration(device.Interval)*time.Second, 5*time.Second)
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 900
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	for {
		time.Sleep(interval)
		if time.Now().After(deadline) {
			return DeviceTokens{}, fmt.Errorf("device code expired (user did not authorize in time)")
		}
		token, next, err := pollDeviceToken(cfg, device.DeviceCode, interval)
		if err != nil {
			return DeviceTokens{}, err
		}
		if token.AccessToken != "" {
			return token, nil
		}
		interval = next
	}
}

func pollDeviceToken(cfg DeviceCodeConfig, deviceCode string, interval time.Duration) (DeviceTokens, time.Duration, error) {
	resp, err := postDeviceForm(cfg, cfg.TokenURL, url.Values{
		"client_id":   {cfg.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
	if err != nil {
		return DeviceTokens{}, interval, fmt.Errorf("poll token: %w", err)
	}
	defer resp.Body.Close()

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
		Interval     int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return DeviceTokens{}, interval, fmt.Errorf("parse poll response (HTTP %d): %w", resp.StatusCode, err)
	}
	if raw.AccessToken != "" {
		return DeviceTokens{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, ExpiresIn: raw.ExpiresIn}, interval, nil
	}
	switch raw.Error {
	case "authorization_pending":
		return DeviceTokens{}, interval, nil
	case "slow_down":
		if raw.Interval > 0 {
			return DeviceTokens{}, time.Duration(raw.Interval) * time.Second, nil
		}
		return DeviceTokens{}, interval + 5*time.Second, nil
	case "":
		return DeviceTokens{}, interval, fmt.Errorf("device flow: HTTP %d with neither a token nor an error", resp.StatusCode)
	}
	if raw.Description != "" {
		return DeviceTokens{}, interval, fmt.Errorf("device flow error: %s: %s", raw.Error, raw.Description)
	}
	return DeviceTokens{}, interval, fmt.Errorf("device flow error: %s", raw.Error)
}

func postDeviceForm(cfg DeviceCodeConfig, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
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
