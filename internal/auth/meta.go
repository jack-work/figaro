package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/term"
)

const (
	metaAuthBase   = "https://auth.meta.com"
	metaClientID   = "1031625952748946"
	MetaAPIBase    = "https://api.meta.ai"
	metaAPIVersion = "1.0.0"
)

var metaDeviceConfig = DeviceCodeConfig{
	ProviderName: "meta",
	ClientID:     metaClientID,
	DeviceURL:    metaAuthBase + "/oidc/device/authorization/",
	TokenURL:     metaAuthBase + "/oidc/device/token/",
	Headers:      map[string]string{"x-api-version": metaAPIVersion},
}

type MuseCodeKey struct {
	APIKey string
	Email  string
}

func LoginMeta() (MuseCodeKey, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("Muse Code signs in with the Meta device flow."))
	fmt.Fprintln(os.Stderr)

	access, err := LoginDeviceCode(metaDeviceConfig, printDevicePrompt)
	if err != nil {
		return MuseCodeKey{}, err
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized with Meta")
	return MintMuseCodeKey(http.DefaultClient, MetaAPIBase, access)
}

func MintMuseCodeKey(client *http.Client, base, accessToken string) (MuseCodeKey, error) {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/muse-code/key",
		bytes.NewReader([]byte(`{"onboard":true}`)))
	if err != nil {
		return MuseCodeKey{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", metaAPIVersion)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	c := *client
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	resp, err := c.Do(req)
	if err != nil {
		return MuseCodeKey{}, fmt.Errorf("muse code key: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return MuseCodeKey{}, fmt.Errorf("muse code key: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw struct {
		APIKey               string `json:"api_key"`
		SubsActive           *bool  `json:"is_subs_active"`
		RequirePayment       bool   `json:"require_payment"`
		ActionURL            string `json:"action_url"`
		RequirePaymentAction string `json:"require_payment_action_url"`
		Email                string `json:"user_email"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return MuseCodeKey{}, fmt.Errorf("muse code key: parse response: %w", err)
	}
	if raw.SubsActive != nil && !*raw.SubsActive {
		return MuseCodeKey{}, fmt.Errorf("muse code key: the subscription on this Meta account is inactive")
	}
	key := strings.TrimSpace(raw.APIKey)
	if key == "" {
		action := strings.TrimSpace(raw.ActionURL + raw.RequirePaymentAction)
		if raw.RequirePayment || action != "" {
			return MuseCodeKey{}, fmt.Errorf("muse code key: a Muse Code subscription is required %s", action)
		}
		return MuseCodeKey{}, fmt.Errorf("muse code key: response carried no api_key")
	}
	return MuseCodeKey{APIKey: key, Email: strings.ToLower(strings.TrimSpace(raw.Email))}, nil
}
