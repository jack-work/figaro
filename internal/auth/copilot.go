package auth

import (
	"context"
	"fmt"
)

const copilotClientID = "Iv1.b507a08c87ecfe98"

func copilotDeviceConfig(domain string) DeviceCodeConfig {
	if domain == "" {
		domain = "github.com"
	}
	return DeviceCodeConfig{
		ProviderName: "copilot",
		ClientID:     copilotClientID,
		Scope:        "read:user",
		DeviceURL:    fmt.Sprintf("https://%s/login/device/code", domain),
		TokenURL:     fmt.Sprintf("https://%s/login/oauth/access_token", domain),
	}
}

func LoginCopilot(ctx context.Context, domain string, c Console) (string, error) {
	c.Note("GitHub Copilot uses the device code flow.")
	token, err := LoginDeviceCode(ctx, copilotDeviceConfig(domain), c)
	if err != nil {
		return "", err
	}
	c.Done("Authorized with GitHub")
	return token.AccessToken, nil
}

func showDevicePrompt(c Console, p DevicePrompt) {
	if p.Prefilled {
		c.Note("Open this URL and confirm the code matches:")
	} else {
		c.Note("Open this URL and enter the code:")
	}
	c.Link(p.VerificationURI)
	c.Code(p.UserCode)
	c.Note("Waiting for authorization...")
}
