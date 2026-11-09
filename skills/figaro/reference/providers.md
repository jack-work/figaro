# Providers: routing a model to a transport

`copilot`, `gateway`, `openrouter`, `azure` and every declared provider are
one provider, `internal/provider/routed`, given different specs. A spec names a host, the headers to send, and how
to choose a transport for each model. `anthropic` (native OAuth) is separate.

## Transports

| Name | Wire | Endpoint |
|---|---|---|
| `messages` | Anthropic Messages | `base_url` + `messages_path` (default `/messages`) |
| `chat` | OpenAI Chat Completions | `base_url` + `/chat/completions` |
| `responses` | OpenAI Responses, HTTP SSE | `base_url` + `/responses` |
| `responses-ws` | OpenAI Responses, WebSocket | `ws(s)://` `base_url` + `/responses` |

## A stream cut mid-reply

Requests that fail before the body starts are retried by the transport. A
stream that ends before its last event (`message_stop` on `messages`, a
finish reason or `[DONE]` on `chat`, `response.completed` on `responses` and
`responses-ws`) is a stream cut: an HTTP/2 reset, a dropped connection or
socket, a clean close too early. The error names the provider, the events
read and the cause. If no tool from that reply has
started, the turn keeps what streamed (the prose; a tool call still being
written is dropped), appends a `continue:` notice from `angelus` saying what
was cut, and sends again after 1 s, then 3 s. A third cut in the same turn
fails it. A tool that already started is never sent twice: that cut fails the
turn as before.

A request that gets no byte at all (no headers, or headers and then nothing)
within `system.first_byte_timeout` (a Go duration, default `90s`; `0` turns
it off) is a stream cut with no events, and is resent the same way. A
keep-alive line counts as a byte. Raise it for a model on the `chat` wire that
thinks for minutes before it sends anything. The idle timer between events is
separate.

## Images, per model

Each model on a provider takes images or text only. figaro reads the
model's `images` field in the catalog (below): `[models."<id>"] text_only`
in the provider file, then what the provider's catalog says
(`capabilities.image_input.supported`, Copilot's `supports.vision`), then a
fact in figaro's mapping. When none says, the provider-wide `text_only`
decides, and without it images are sent. A text-only model gets each image
part replaced with a note naming what decided it, on every transport, at
send time; the aria's history keeps its images for the next model.

When nothing says and the provider refuses the images (rou2's chat route:
400 `unsupported_capability`, "carries an image, and the selected model
route accepts text only"), figaro sends once more with the notes in their
place, and treats the model as text-only (`src` `refusal` in `@models`)
until the daemon restarts. Only `text_only = false` in the provider file
outranks a refusal.

```toml
[models."explabs/glm-5.3"]
text_only = true
```

## Image size and count, per model

`max_prompt_image_size` (bytes of the decoded image) and `max_prompt_images`
(per request) are fitted before a send when the provider file or a fact in
figaro's mapping states them. An image over the size is sent smaller, with a
note naming the limit and the factor that maps coordinates back; one that
cannot be made that small is replaced with a note. Over the count, the oldest
images are replaced with a note. A provider's own catalog numbers (Copilot
states one image of 3 MiB) are listed but not enforced up front: Copilot took
two images and a 4.5 MB image on such models on 2026-11-08. When a provider
refuses a request over an image's size (Anthropic's wording, directly or
through Copilot's messages route: "image exceeds 10 MB maximum"), figaro downscales to the
catalog's size, or to the size the refusal names, and sends once more; it
never tries a third time.

```toml
[models."explabs/qwen3.8-max"]
max_prompt_image_size = 5242880
max_prompt_images     = 8
```

## The model catalog

Every provider's model list lands in `@models` (`figaro models`, the
`:models` pit) as one shape: `name`, `context` (`default`, `long`),
`max_prompt`, `max_output`, `images`, `max_prompt_image_size`,
`max_prompt_images`, `tools`, `reasoning`, `hidden`, `endpoints`. A field
the provider does not state is absent, not zero. `src` says where each field
came from: `api` (the provider's list), `mapping` (a fact figaro ships) or
`config` (the provider file). The context % uses `context`, the model's
window; routing uses `endpoints`. `max_prompt` is the prompt cap a provider
states (Copilot's `max_prompt_tokens`, below the window): `figaro models`
lists it, nothing enforces it, because Copilot accepts prompts over it.

Each provider reads its list through a mapping: per field, paths into a
list entry, and the first that holds a value of the field's type wins.
copilot, meta, anthropic and openrouter ship their own; anthropic's carries
the context windows its list leaves out as facts. azure and a gateway
preset (rou2, `gateway`) read the generic one, which knows the usual names (`context_length`,
`context_window`, `max_input_tokens`, `max_output_tokens`, `max_tokens`,
`name`, `display_name`, `capabilities.image_input.supported`,
`supports_tools`, `supported_endpoints` and the like), so a gateway that
serves them needs no catalog config. rou2 needs none: its only line is
glm-5.3's `text_only`, which no catalog states.

A provider file can override a model's numbers, and they win over the list.
They also hold for a model the list never names:

```toml
[models."openai/gpt-6-luna"]
context    = 400000        # long = context unless long is given
max_output = 128000
```

A `[catalog]` table replaces keys of the built-in mapping one by one; a key
set to `[]` reads nothing. Rarely needed: for a gateway whose list uses
names the generic mapping does not know.

```toml
[catalog]
list       = "data"                                      # where the array is
context    = ["limits.prompt", "context_length"]
images     = ["architecture.input_modalities has:image"] # array holds the value
reasoning  = ["capabilities.reasoning_effort nonempty"]  # array or object has members
visible    = ["enabled"]                                 # false hides the model

[catalog.facts."glm-5*"]                                 # what the list doesn't say
context = 1048576
```

A path is a dotted key. A fact applies to a model id lower-cased, without
its vendor prefix and with dots as dashes; `glm-5*` matches `glm-5` and
`glm-5-3`, not `glm-50`; the longest pattern wins. A bad path or pattern
fails the provider's build.

## A body cap

A gateway that cuts request bodies at a size (rou2 at 8 MiB, which then
answers "Request body must contain valid JSON") declares it with
`max_request_bytes` in its file. `system.max_request_bytes` overrides it per
aria; `0` means no limit. Over the limit, figaro first sends the oldest
images smaller (re-encoded or resized, with a note giving the factor that maps
coordinates back), and only if that is not enough replaces the oldest image
parts with a note saying so, newest images last. It never touches text. If the text alone is over, the send fails with
"request too large" and names the knob. Only the request changes: the
aria's history keeps its images.

## Route order

1. The first `[[routing.rule]]` whose `model` glob matches.
2. The catalog: `GET base_url + routing.catalog`, then the model's
   `supported_endpoints`. Preference: `responses-ws` > `responses` > `messages` >
   `chat`. A `ws:` prefix marks a socket. One catalog serves every figaro
   on that gateway. A restarted daemon routes from the catalog it persisted
   in `@models`, and refreshes it in the background at start; a catalog older
   than 30 minutes is refreshed beside the turn, never inside it. A turn
   waits for the catalog only when it knows nothing yet and the model is not
   in it with no default to fall back on, and then for at most 3 s. With a
   default, a catalog that is missing or fails is logged and skipped, and
   asked again after five minutes.
3. `routing.default`.
4. Otherwise the send is refused, and the error names all three.

## The file: `providers/<name>.toml`

```toml
base_url      = "http://localhost:4000/v1"
api_key       = "..."                       # or the provider's env var
cache_markers = "trusted"                   # chat only; see cache-control.md
messages_path = "/v1/messages"              # when base_url does not end in /v1
text_only     = true                        # every model: send images as a text stand-in
max_request_bytes = 8323072                 # the gateway's body cap, less headroom
headers       = { "X-Team" = "figaro" }     # every request, every transport

[transport_headers.responses]               # one transport only
"X-Experiment" = "1"

[routing]
catalog = "/models"                         # "off" for none; the gateway preset defaults to /models
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
| `gateway` | catalog `/models`; default `chat`; credential optional | everything. `base_url` is required; `FIGARO_GATEWAY_BASE_URL` wins over the file. |
| `openrouter` | default `chat`; OpenRouter host; `anthropic/` model prefix | everything. |
| `azure` | default `chat`; credential from `AZURE_OPENAI_API_KEY`, a stored key, or a Microsoft Entra login | everything. `base_url` is required; `AZURE_BASE_URL` wins over the file. |

## Copilot: models, credentials, Responses settings

The `copilot` provider routes models by the capability advertised in the
Copilot catalog. Claude-compatible models use the Anthropic Messages
transport; Responses-capable models such as `gpt-5.6-terra` use Figaro's
native WebSocket Responses transport. Figaro does not start a Copilot CLI
process.

```bash
figaro models
```

For noninteractive use, Copilot credentials are read in this order:
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, then `GITHUB_TOKEN`.

Choose a catalog model in an outfit:

```toml
[system]
provider = "copilot"
model = "gpt-5.6-terra"
context_tier = "long_context"
thinking_effort = "high"
reasoning_context = "all_turns"
reasoning_summary = "auto"
verbosity = "low"
max_tokens = 16000
```

Responses settings can change between turns on a live figaro:

```bash
figaro set system.model '"gpt-5.6-luna"'
figaro set system.context_tier '"default"'
figaro set system.reasoning_context '"current_turn"'
figaro set system.reasoning_summary '"auto"'
figaro set system.max_context_tokens 120000
figaro set system.parallel_tool_calls false
figaro set system.temperature 0.4
```

`system.context_tier` selects the catalog's default or long-context replay
budget; `system.max_context_tokens` can impose a smaller cap. Figaro rejects
a turn that would exceed that budget rather than dropping cached history.
`system.reasoning_context` maps to the Responses API's `auto`,
`current_turn`, or `all_turns` mode. `system.reasoning_summary` accepts
`"auto"`, `"concise"`, or `"detailed"` and requests a readable reasoning
summary; it does not expose raw private chain-of-thought.
`system.temperature` and `system.top_p` are mutually exclusive. A model
switch starts a new Responses cache lineage so opaque reasoning is never
replayed under a different model.

## What CTX counts

`figaro status` and the `CTX` column in `figaro list` report **context used**:
the last turn's prompt plus that turn's output, i.e.

```
input_tokens + cache_read_input_tokens + cache_creation_input_tokens + output_tokens
```

All three input buckets count: with prompt caching on (the default) most of
the prompt comes back as a cache read, so summing only `input_tokens` would
under-report a long figaro by orders of magnitude. Messages appended after the
last metered turn are estimated at chars/4 and the figure is prefixed `~`.

The window it is measured against comes from the provider (Anthropic reports
`max_input_tokens` on `/v1/models`; `figaro models` shows the same numbers).
`system.max_context_tokens` overrides it in either direction:

```bash
figaro set system.max_context_tokens 200000   # pin the window
figaro unset system.max_context_tokens        # back to the provider's number
```

Unknown models report no window rather than a guessed one, and status then
prints a bare token count.

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
scopes    = ["openid", "profile", "groups"]   # the device flow adds offline_access
flow      = "device"                       # the default, or "pkce" (below)
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
name: the login runs the flow against the issuer and hands the pair to
hush, which refreshes it with its `refresh_token` grant and stores each rotated
refresh token. Endpoints off the issuer's origin are refused, the issuer must
be https, and every issuer call carries a `figaro/<version>` User-Agent. No
audience is sent.

`flow = "pkce"` is for an IdP with no device flow (Kanidm): the authorization
code grant with PKCE (S256) as a public client. The login listens on
`127.0.0.1` at a port the OS picks, opens `$BROWSER` or the platform opener on
the authorize URL (and prints it), and waits up to five minutes for the
redirect to `http://127.0.0.1:<port>/callback`, an IP literal rather than
`localhost` per RFC 8252. The IdP must accept any loopback port for the client
(Kanidm: `enable-localhost-redirects` on a public client). A redirect whose
`state` is not this login's is answered 400 and the login keeps waiting.
Scopes go out as written, with no `offline_access` added, and a login that
gets no `refresh_token` fails, since hush could not keep it fresh.

Without `[oauth]`, the provider takes `api_key`, or the variable
`api_key_env` names, and may run keyless, as `gateway` does.

The key is looked for in this order (`boot.BuildResolver`): the environment;
`api_key_file` (relative to `$CREDENTIALS_DIRECTORY` when not absolute; a
named file that is missing or empty is an error); `$CREDENTIALS_DIRECTORY/<provider>`;
the stored `api_key`, decrypted through hush when it is `AGE-ENC[...]`; then
`[oauth]` through hush.

### A service token instead of a login

A daemon with no user to log in (a container, a server) can authenticate as an
OAuth client with the client_credentials grant:

```toml
# providers/rou2.toml
preset        = "gateway"
base_url      = "http://10.233.1.1:9110/u/explabs/v1"
service_token = "rou2"

# config.toml
[service_tokens.rou2]
issuer            = "https://auth.kelliher.info"   # or token_url
client_id         = "figaro"
secret_credential = "authelia-client-secret"       # a file in $CREDENTIALS_DIRECTORY
audience          = ["https://rou2.kelliher.info"]
```

`service_token` replaces every other source: a failed mint is an error, never a
keyless request, and it cannot sit beside `[oauth]`. The daemon mints one token
per client and shares it across figaros, renews it a minute before expiry, and
mints again when the gateway answers 401. `provider login` refuses such a
provider; `provider ls` shows the client and flags a missing secret.

An IdP that grants no client_credentials (Kanidm) may still exchange a
service account's long-lived API token for a short access token, by RFC 8693
token exchange:

```toml
[service_tokens.rou2]
grant              = "token-exchange"
issuer             = "https://idm.example/oauth2/openid/rou2"   # or token_url
client_id          = "rou2"
subject_credential = "rou2-api-token"             # a file in $CREDENTIALS_DIRECTORY
scopes             = ["openid", "groups", "profile"]
# subject_token_type = "urn:ietf:params:oauth:token-type:access_token" (the default)
```

The client is public: `client_id` goes in the body and nothing else
authenticates it, so `secret_credential` and `auth_method` are refused (Kanidm
answers `invalid_request` to a secret on an exchange). The subject is read
from its file at every exchange and held nowhere else. Caching, sharing,
renewal and the retry on 401 are client_credentials'. systemd copies a
`LoadCredential=` file when the unit starts, so a rotated subject reaches the
daemon at its next restart.

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
| `service_token` | kept: the daemon mints it from config |
| the environment | kept: figaro cannot unset its parent's variables |

It names each source it kept and exits 1 while one would still sign figaro in.
It removes figaro's copy only: a token the provider issued, or a key it minted
(`meta`), stays valid there until revoked with the provider. Nothing caches a
token past it, so the daemon's next request already goes without.

## Not a provider: decision models

A decision model (jev, decision-1) is never a figaro's `system.model`; setting
one is refused. It is asked with `figaro decide`, which sends through an
ordinary provider named by `system.decide.provider`. See
[decisions.md](decisions.md).

## Translation channels

`chat` writes `<name>`, `messages` writes `<name>-messages`, and both Responses
transports write `<name>-responses`. The names predate routing, so existing figaros
keep their caches. Entries are translated as they land only when routing is
static (a default, no catalog, no rules); otherwise they are translated at send
time, once the model's transport is known. A gateway is static only with
`catalog = "off"` and no rules.

## Checking a route

`figaro doctor provider --id <id> -j` files Responses streams as `WS` or `SSE`
and HTTP round-trips by path. `figaro models` lists catalog models that have a
route.
