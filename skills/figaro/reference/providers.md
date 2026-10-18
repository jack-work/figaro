# Providers: routing a model to a transport

`copilot`, `gateway`, `openrouter`, `azure` and every declared provider are
one provider, `internal/provider/routed`, given different specs. A spec names a host, the headers to send, and how
to choose a transport for each model. `anthropic` (native OAuth) is separate.

## Transports

| Name | Wire | Endpoint |
|---|---|---|
| `messages` | Anthropic Messages | `base_url` + `messages_path` (default `/v1/messages`) |
| `chat` | OpenAI Chat Completions | `base_url` + `/chat/completions` |
| `responses` | OpenAI Responses, HTTP SSE | `base_url` + `/responses` |
| `responses-ws` | OpenAI Responses, WebSocket | `ws(s)://` `base_url` + `/responses` |

## Route order

1. The first `[[routing.rule]]` whose `model` glob matches.
2. The catalog: `GET base_url + routing.catalog`, then the model's
   `supported_endpoints`. Preference: `responses-ws` > `responses` > `messages` >
   `chat`. A `ws:` prefix marks a socket.
3. `routing.default`.
4. Otherwise the send is refused, and the error names all three.

## The file: `providers/<name>.toml`

```toml
base_url      = "http://localhost:4000/v1"
api_key       = "..."                       # or the provider's env var
cache_markers = "trusted"                   # chat only; see cache-control.md
messages_path = "/messages"                 # when base_url already ends in /v1
headers       = { "X-Team" = "figaro" }     # every request, every transport

[transport_headers.responses]               # one transport only
"X-Experiment" = "1"

[routing]
catalog = "/models"                         # optional
default = "chat"

[[routing.rule]]                            # file rules run before the preset's
model     = "grok-*"
transport = "responses"
```

The provider fails to build, rather than guess, on an unknown transport, a rule
with no transport, a bad glob, or a spec with no way to route at all.

## Presets

| Provider | Built in | The file adds |
|---|---|---|
| `copilot` | catalog `/models`; rule `claude-*` → `messages`; Copilot and identity headers; host from the hush token | rules, headers. `base_url` stays Copilot's own override (`token_mode`, `enterprise_domain`). |
| `gateway` | default `chat`; credential optional | everything. `base_url` is required; `FIGARO_GATEWAY_BASE_URL` wins over the file. |
| `openrouter` | default `chat`; OpenRouter host; `anthropic/` model prefix | everything. |
| `azure` | default `chat`; credential from `AZURE_OPENAI_API_KEY`, a stored key, or a Microsoft Entra login | everything. `base_url` is required; `AZURE_BASE_URL` wins over the file. |

## Azure AI Foundry, signed in with Microsoft Entra

```sh
figaro provider login azure            # asks for the endpoint and tenant once
figaro set system.provider azure
figaro set system.model <deployment>   # the deployment name, not the model family
```

The login is Entra's device flow: a code to enter at Microsoft's device page,
then hush holds the tokens and refreshes them with its `entra` grant, which
names the scope on every refresh. A hush agent without that grant refuses the
registration, and the login says to update hush.

`providers/azure.toml` keeps what the login asked for. Every key but
`base_url` is optional:

```toml
base_url  = "https://<resource>.openai.azure.com/openai/v1"
tenant    = "contoso.onmicrosoft.com"   # or the tenant id; default organizations
client_id = "..."                        # default: the Azure CLI's public client
scope     = "https://ai.azure.com/.default"   # default: Cognitive Services
authority = "https://login.microsoftonline.us" # sovereign clouds only
```

The account needs the Cognitive Services OpenAI User role on the resource. A
tenant whose policy blocks the Azure CLI's client needs an app registration of
its own (a public client with the Cognitive Services delegated permission),
named by `client_id`.

## Declaring a provider: a gateway of your own, with OAuth

A file in `providers/` that names a `preset` creates a provider of that name.
No Go code per provider; the file is the registration.

```toml
# providers/house.toml
preset   = "gateway"                       # chat, OpenAI-compatible
base_url = "http://100.103.40.20:9110/v1"

[oauth]                                    # optional; makes `figaro provider login house` work
issuer    = "https://auth.example"         # its /.well-known/openid-configuration is read
client_id = "house"
scopes    = ["openid", "profile", "groups"]   # offline_access is always added
flow      = "device"                       # the only flow so far, and the default
# discovery = "http://.../.well-known/..." # fills issuer, client_id, scopes the file leaves out
```

```sh
figaro provider login house
figaro set system.provider house
figaro set system.model <model>
```

`gateway` is the only preset. Every key the `gateway` row above takes works
here too (`cache_markers`, `headers`, `routing`), and `<NAME>_BASE_URL` wins
over `base_url`. A preset on a built-in's name is ignored with a warning. Files
are read when figaro loads its config; a running daemon picks up a new one when
it restarts.

With `[oauth]`, the credential is the token hush holds under the provider's
name: the login runs the device flow against the issuer and hands the pair to
hush, which refreshes it with its `refresh_token` grant and stores each rotated
refresh token. Endpoints off the issuer's origin are refused, the issuer must
be https, and every issuer call carries a `figaro/<version>` User-Agent. No
audience is sent. Without `[oauth]`, the provider takes `api_key`, or the
variable `api_key_env` names, and may run keyless, as `gateway` does.

The key is looked for in this order (`boot.BuildResolver`): the environment;
`api_key_file` (relative to `$CREDENTIALS_DIRECTORY` when not absolute; a
named file that is missing or empty is an error); `$CREDENTIALS_DIRECTORY/<provider>`;
the stored `api_key`, decrypted through hush when it is `AGE-ENC[...]`; then
`[oauth]` through hush.

## Credentials

```sh
figaro provider ls                 every provider, the source in use, and the others
figaro provider ls copilot         one provider's sources in order, and who owns each
figaro provider login copilot      sign in
figaro provider logout copilot     remove figaro's copy
```

`login` runs the provider's own flow: OAuth for `anthropic`, a device code for
`copilot`, `meta` and `azure`, and `[oauth]` for a declared provider. A provider
with no flow (`openrouter`, `gateway`) asks for an API key and seals it into
`providers/<name>.toml` as `AGE-ENC[...]`.

`ls` lists sources in the order above, so the first is the one in use. `copilot`
is the exception: unless `token_mode = "direct"`, it uses only the credential
hush holds, and its other sources are what would be handed to hush again the
next time that credential is missing.

`logout` removes what figaro wrote and nothing else:

| Source | logout |
|---|---|
| the hush credential (OAuth, device code, copilot's exchange) | deleted from hush |
| a sealed `api_key` (`AGE-ENC[...]`) | its line removed from `providers/<name>.toml`; the file goes when nothing else is in it |
| a plaintext `api_key` | kept: you wrote it |
| `api_key_file`, `$CREDENTIALS_DIRECTORY/<name>` | kept: the file or the service manager owns it |
| the environment | kept: figaro cannot unset its parent's variables |

It names each source it kept and exits 1 while one would still sign figaro in.
It removes figaro's copy only: a token the provider issued, or a key it minted
(`meta`), stays valid there until revoked with the provider. Nothing caches a
token past it, so the daemon's next request already goes without.

## Not a provider: decision models

A decision model (jev, decision-1) is never an aria's `system.model`; setting
one is refused. It is asked with `figaro decide`, which sends through an
ordinary provider named by `system.decide.provider`. See
[decisions.md](decisions.md).

## Translation channels

`chat` writes `<name>`, `messages` writes `<name>-messages`, and both Responses
transports write `<name>-responses`. The names predate routing, so existing arias
keep their caches. Entries are translated as they land only when routing is
static (a default, no catalog, no rules); otherwise they are translated at send
time, once the model's transport is known.

## Checking a route

`figaro doctor provider --id <id> -j` files Responses streams as `WS` or `SSE`
and HTTP round-trips by path. `figaro models` lists catalog models that have a
route.
