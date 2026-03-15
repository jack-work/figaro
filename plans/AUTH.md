# Figaro Auth: Encrypted Token Management via Hush

## Overview

Figaro stores OAuth tokens (refresh + access) encrypted at rest using
[hush](https://github.com/jack-work/hush). The hush agent must be running
for figaro to start — figaro never touches raw key material on disk.

This design is isolated behind an `auth` package so it can later be
extracted into hush itself as a "managed token refresh" feature.

## Dependencies

- `github.com/jack-work/hush/client` — library client for encrypt/decrypt
  via the running hush agent's unix socket
- No dependency on hush internals (agent, identity, config) — only the
  client package

## Storage

Tokens live in a TOML file at `$XDG_CONFIG_HOME/figaro/auth.toml`
(default `~/.config/figaro/auth.toml`):

```toml
[anthropic]
access_token = "AGE-ENC[...]"
refresh_token = "AGE-ENC[...]"
expires_at = 1741830000000
```

- `access_token`: short-lived OAuth access token (~1 hour), encrypted
- `refresh_token`: long-lived refresh token, encrypted
- `expires_at`: unix millis, plaintext (not sensitive, needed to check
  expiry without decrypting)

Both tokens are individually age-encrypted via hush. The file itself is
readable (`0600`), but the values are opaque ciphertext.

## Components

### `internal/auth/auth.go` — Token Manager

The core abstraction. Isolated so it could move to hush later.

```go
// TokenManager handles encrypted storage and auto-refresh of OAuth tokens.
type TokenManager interface {
    // AccessToken returns a valid access token, refreshing if expired.
    // Requires hush agent to be running.
    AccessToken() (string, error)
}
```

Internally:

```go
type OAuthConfig struct {
    ProviderName string            // "anthropic" — key in auth.toml
    TokenURL     string            // refresh endpoint
    ClientID     string            // OAuth client ID
    BuildRefreshRequest func(refreshToken string) RefreshRequest
}

type manager struct {
    hush     *client.Client        // hush agent socket client
    config   OAuthConfig
    filePath string                // path to auth.toml
    cached   string                // in-memory access token (plaintext)
    expires  int64                 // cached expiry (unix millis)
}
```

#### `AccessToken()` flow

```
1. Is cached token still valid (in memory, not expired)?
   YES → return cached
   NO  → continue

2. Ping hush agent
   FAIL → return error: "hush agent is not running. Start it: hush up -d"

3. Read auth.toml, get encrypted refresh_token and expires_at

4. Is expires_at in the future (with 5-minute buffer)?
   YES → decrypt access_token via hush, cache in memory, return it
   NO  → continue to refresh

5. Decrypt refresh_token via hush

6. POST to TokenURL with refresh_token
   → receive new access_token, refresh_token, expires_in

7. Encrypt both new tokens via hush

8. Write to auth.toml:
   - access_token = AGE-ENC[new_access]
   - refresh_token = AGE-ENC[new_refresh]
   - expires_at = now + expires_in - 5min buffer

9. Cache new access_token in memory, return it
```

### `internal/auth/anthropic.go` — Anthropic OAuth Config

```go
var AnthropicOAuth = OAuthConfig{
    ProviderName: "anthropic",
    TokenURL:     "https://console.anthropic.com/v1/oauth/token",
    ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
    BuildRefreshRequest: func(refreshToken string) RefreshRequest {
        return RefreshRequest{
            GrantType:    "refresh_token",
            ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
            RefreshToken: refreshToken,
        }
    },
}
```

### `figaro login` — Initial Token Acquisition

A CLI subcommand that performs the full OAuth PKCE flow:

```
1. Ping hush agent
   FAIL → exit: "hush agent is not running. Start it: hush up -d"

2. Generate PKCE verifier + challenge (S256)

3. Build authorization URL:
   https://claude.ai/oauth/authorize?
     client_id=...&
     response_type=code&
     redirect_uri=https://console.anthropic.com/oauth/code/callback&
     scope=org:create_api_key user:profile user:inference&
     code_challenge=...&
     code_challenge_method=S256

4. Open URL in browser (xdg-open / open)

5. Prompt user: "Paste the authorization code:"

6. Exchange code for tokens:
   POST https://console.anthropic.com/v1/oauth/token
   { grant_type: "authorization_code", client_id, code, state,
     redirect_uri, code_verifier }
   → { access_token, refresh_token, expires_in }

7. Encrypt both tokens via hush

8. Write to ~/.config/figaro/auth.toml

9. Print: "Logged in. Access token expires in {expires_in}."
```

### Anthropic Provider — OAuth-Aware Headers

When the Anthropic provider detects an OAuth token (`sk-ant-oat-*`),
it switches behavior:

| Aspect | API Key | OAuth |
|---|---|---|
| Auth header | `x-api-key: <key>` | `Authorization: Bearer <token>` |
| Beta header | `fine-grained-tool-streaming-...` | + `claude-code-20250219,oauth-2025-04-20` |
| User-Agent | (default) | `claude-cli/2.1.62` |
| Extra header | — | `x-app: cli` |
| System prompt | as-is | Prepend: `"You are Claude Code, Anthropic's official CLI for Claude."` |
| Tool names | as-is (`bash`) | Map to CC casing (`Bash`), map back on response |

This detection happens in the provider at `Send()` time. The provider
receives the access token string and checks its prefix.

## Integration with Agent Loop

The provider does NOT call `auth.AccessToken()` itself. The agent loop
(or the CLI entrypoint) resolves the token before constructing the
provider:

```go
// cmd/figaro/main.go
tokenMgr := auth.NewManager(hushClient, auth.AnthropicOAuth, authFilePath)
token, err := tokenMgr.AccessToken()  // may refresh, may decrypt
if err != nil { fatal(err) }

prov := anthropic.New(token, *model)   // provider gets a plain string
```

When figaro becomes a daemon (step 3), the daemon calls
`tokenMgr.AccessToken()` before each `Send()` — the manager handles
refresh transparently.

## Isolation for Future Extraction

The `auth` package has no figaro-specific logic. It depends on:
- `github.com/jack-work/hush/client` — encrypt/decrypt
- `net/http` — token refresh HTTP call
- `os` / `toml` — file I/O

It could be extracted into hush as `hush/oauth` or `hush/token` with
zero changes. The `OAuthConfig` struct makes it provider-agnostic —
pass a different config for GitHub, Google, etc.

## File Layout

```
internal/auth/
├── auth.go        — TokenManager interface + manager implementation
├── anthropic.go   — AnthropicOAuth config constant
├── login.go       — PKCE login flow (used by `figaro login`)
└── pkce.go        — PKCE verifier/challenge generation
```

## Error Cases

| Condition | Behavior |
|---|---|
| Hush agent not running | Error: `"hush agent is not running. Start it: hush up -d"` |
| No auth.toml exists | Error: `"not logged in. Run: figaro login"` |
| Refresh token expired/revoked | Error: `"refresh failed. Run: figaro login"` |
| Network error during refresh | Error with details, keep old tokens on disk |
| Decrypt fails | Error (likely hush identity mismatch) |

## Sequence Diagram

```
User          CLI            Auth Manager       Hush Agent      Anthropic
  │            │                  │                  │              │
  ├─ figaro login ──────────────►│                  │              │
  │            │                  ├── ping ─────────►│              │
  │            │                  │◄── ok ───────────┤              │
  │            │  open browser ◄──┤                  │              │
  │  paste code ─────────────────►│                  │              │
  │            │                  ├── exchange code ──┼─────────────►│
  │            │                  │◄── tokens ───────┼──────────────┤
  │            │                  ├── encrypt ───────►│              │
  │            │                  │◄── AGE-ENC ──────┤              │
  │            │                  ├── write auth.toml │              │
  │            │  "logged in" ◄───┤                  │              │
  │            │                  │                  │              │
  ├─ figaro "fix bug" ──────────►│                  │              │
  │            │   AccessToken() ►│                  │              │
  │            │                  ├── expired? ──────┤              │
  │            │                  ├── decrypt refresh►│              │
  │            │                  │◄── plaintext ────┤              │
  │            │                  ├── POST /token ───┼─────────────►│
  │            │                  │◄── new tokens ───┼──────────────┤
  │            │                  ├── encrypt ───────►│              │
  │            │                  ├── write auth.toml │              │
  │            │◄── access token ─┤                  │              │
  │            │                  │                  │              │
  │            ├── anthropic.New(token) ─────────────┼─────────────►│
  │            │◄── streaming response ──────────────┼──────────────┤
```
