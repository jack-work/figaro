package auth

import (
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/term"
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

func LoginCopilot(domain string) (string, error) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("GitHub Copilot uses the device code flow."))
	fmt.Fprintln(os.Stderr)

	token, err := LoginDeviceCode(copilotDeviceConfig(domain), printDevicePrompt)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(os.Stderr, "       "+term.Green("\u2713")+" Authorized with GitHub")
	return token, nil
}

func printDevicePrompt(p DevicePrompt) error {
	if p.Prefilled {
		fmt.Fprintln(os.Stderr, "       "+term.Dim("Open this URL and confirm the code matches:"))
	} else {
		fmt.Fprintln(os.Stderr, "       "+term.Dim("Open this URL and enter the code:"))
	}
	fmt.Fprintln(os.Stderr, "       "+term.Cyan(p.VerificationURI))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       Code: "+term.Cyan(p.UserCode))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "       "+term.Dim("Waiting for authorization..."))
	return nil
}
