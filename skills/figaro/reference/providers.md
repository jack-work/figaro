# Providers: routing a model to a transport

`copilot`, `gateway`, `openrouter`, `azure` and `rou2` are one provider,
`internal/provider/routed`, given five different specs. A spec names a host, the headers to send, and how
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
| `rou2` | default `chat`; per-block cache markers, no top-level directive; credential from an Authelia login | everything. `base_url` is required and the login writes it; `ROU2_BASE_URL` wins over the file. |

## Azure AI Foundry, signed in with Microsoft Entra

```sh
figaro login azure                     # asks for the endpoint and tenant once
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

## rou2, the house gateway, signed in with Authelia

```sh
ROU2_BASE_URL=http://100.103.40.20:9110 figaro login rou2   # or answer the prompt
figaro set system.provider rou2
figaro set system.model explabs/<model>   # qualified: the routed door picks the upstream
```

The login reads `/.well-known/rou2` for the issuer, client id and scopes, then
the issuer's own OpenID discovery for the device and token endpoints; nothing
about Authelia is compiled in. Endpoints off the issuer's origin are refused.
hush holds the tokens and refreshes them with its default `refresh_token`
grant. The login writes `base_url = "<rou2>/v1"` to `providers/rou2.toml`.

`figaro decide` reaches jev through it with `provider = "rou2"`,
`base_url = "<rou2>/v1"` and `model = "explabs/jev-latest"` in
`providers/decisions.toml`.

## Not a provider: decision models

A decision model (jev) is never an aria's `system.model`; setting one is
refused. It is asked with `figaro decide`, configured by
`providers/decisions.toml`. See [decisions.md](decisions.md).

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
