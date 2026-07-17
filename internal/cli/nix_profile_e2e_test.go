package cli

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNixProfileCopilotE2E(t *testing.T) {
	if os.Getenv("FIGARO_NIX_E2E_TEST") != "1" {
		t.Skip("set FIGARO_NIX_E2E_TEST=1 to exercise the installed Nix profile with the local Copilot credential")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the Nix profile harness runs through WSL from Windows")
	}

	wsl, err := exec.LookPath("wsl.exe")
	require.NoError(t, err)

	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	loaded := mustLoadConfig()
	resolver, err := buildResolver(loaded, "copilot")
	require.NoError(t, err)
	token, err := resolver.Resolve()
	require.NoError(t, err)
	client, _ := buildProvider(loaded, "copilot")
	require.NotNil(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := client.Models(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, models)
	var copilotConfig struct {
		EnterpriseDomain string `toml:"enterprise_domain"`
	}
	require.NoError(t, loaded.LoadProviderAuth("copilot", &copilotConfig))

	const script = `set -eu
bin="$HOME/.nix-profile/bin/figaro"
test -x "$bin"
root=$(mktemp -d)
export FIGARO_CONFIG_DIR="$root/config"
export FIGARO_RUNTIME_DIR="$root/runtime"
export FIGARO_STATE_DIR="$root/state"
export FIGARO_HUSH_DIR="$root/hush"
export FIGARO_HUSH_PASSPHRASE="figaro-nix-e2e"
cleanup() {
  "$bin" stop --force >/dev/null 2>&1 || true
  rm -rf "$root"
}
trap cleanup EXIT
test -n "${COPILOT_GITHUB_TOKEN:-}"
mkdir -p "$FIGARO_CONFIG_DIR/loadouts" "$FIGARO_CONFIG_DIR/providers"
if [ -n "${COPILOT_ENTERPRISE_DOMAIN:-}" ]; then
  printf 'enterprise_domain = "%s"\n' "$COPILOT_ENTERPRISE_DOMAIN" > "$FIGARO_CONFIG_DIR/providers/copilot.toml"
fi
cat > "$FIGARO_CONFIG_DIR/config.toml" <<'EOF'
default_loadout = "gpt-e2e"
EOF
cat > "$FIGARO_CONFIG_DIR/loadouts/gpt-e2e.toml" <<'EOF'
[system]
provider = "copilot"
model = "gpt-5.6-terra"
context_tier = "long_context"
reasoning_context = "all_turns"
reasoning_summary = "detailed"
thinking_effort = "max"
max_tokens = 16000
EOF
created=$("$bin" new -j -- "Reply with exactly: NIX_PROFILE_BOOT_OK")
aria=$(printf '%s\n' "$created" | sed -n 's/.*"aria_id":"\([^"]*\)".*/\1/p')
test -n "$aria"
result=$("$bin" send --id "$aria" -r -t -- "Use the bash tool exactly once to run: echo NIX_PROFILE_TOOL_OK. After it finishes, reply with exactly: NIX_PROFILE_GPT_E2E_OK.")
printf 'nix-send-bytes=%s\n' "${#result}"
printf '%s\n' "$result"
if ! printf '%s\n' "$result" | grep -q 'NIX_PROFILE_GPT_E2E_OK'; then
  "$bin" show --id "$aria" 8 -j -l >&2 || true
fi
`
	cmd := exec.Command(wsl, "-d", "nixos", "--exec", "/bin/sh", "-c", script)
	wslEnv := os.Getenv("WSLENV")
	hasToken := false
	hasEnterpriseDomain := false
	for _, entry := range strings.Split(wslEnv, ":") {
		if strings.SplitN(entry, "/", 2)[0] == "COPILOT_GITHUB_TOKEN" {
			hasToken = true
		}
		if strings.SplitN(entry, "/", 2)[0] == "COPILOT_ENTERPRISE_DOMAIN" {
			hasEnterpriseDomain = true
		}
	}
	if !hasToken {
		if wslEnv != "" {
			wslEnv += ":"
		}
		wslEnv += "COPILOT_GITHUB_TOKEN"
	}
	if !hasEnterpriseDomain {
		if wslEnv != "" {
			wslEnv += ":"
		}
		wslEnv += "COPILOT_ENTERPRISE_DOMAIN"
	}
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "COPILOT_GITHUB_TOKEN=") || strings.HasPrefix(entry, "COPILOT_ENTERPRISE_DOMAIN=") || strings.HasPrefix(entry, "WSLENV=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env,
		"COPILOT_GITHUB_TOKEN="+token,
		"COPILOT_ENTERPRISE_DOMAIN="+copilotConfig.EnterpriseDomain,
		"WSLENV="+wslEnv,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "NIX_PROFILE_TOOL_OK")
	require.Contains(t, string(output), "NIX_PROFILE_GPT_E2E_OK")
}
