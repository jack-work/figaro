# Providers: routing a model to a transport

`copilot`, `gateway` and `openrouter` are one provider, `internal/provider/routed`,
given three different specs. A spec names a host, the headers to send, and how
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
