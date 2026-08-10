# Cache control

Anthropic's prompt cache lets up to **four** `cache_control` breakpoints mark
cached prefixes; everything before a mark is reused on later turns at a
fraction of the input-token cost. Figaro applies this **automatically** — you
rarely need to touch it.

## What happens by default

Caching is **on by default at short (5m) ephemeral retention**, in every
Anthropic-family provider. Each turn, `markCacheBreakpoints` stamps three
breakpoints:

- the last **system** block (the credo/identity prefix),
- the last **tool**,
- the leaf of the **last input message** (the rolling tail).

So the static prefix is a cache *read* every turn after the first, and the
rolling breakpoint caches the growing transcript so the next turn reads all
prior history. That leaves one of the four breakpoints free — deliberately,
for a downstream gateway that adds its own. **The tail is stamped last in
wire order** and must stay that way: Anthropic honours all four breakpoints,
but a gateway lowering them to Gemini keeps only the final one.

Confirm it's working from a `FIGARO_WIRE_DIR` dump: a follow-up turn shows
`cache_read_input_tokens > 0`.

## Overriding

`system.cache_control` on the form overrides the default:

```
figaro set system.cache_control none      # stop signalling (see below)
figaro set system.cache_control 1h        # long (1h) retention
figaro set system.cache_control ephemeral # explicit short (the default)
```

**`none` stops figaro signalling; it does not stop the provider caching.**
The distinction is the provider's, not ours, and it was measured:

- **Anthropic-family** (anthropic, anthropicsdk, copilot serving Claude)
  caches only what you mark. With `none`, a turn whose prompt had been
  reading 8,833 tokens from cache reported **9,118 uncached input tokens and
  a cache-read delta of exactly 0**. Here `none` really does turn caching
  off.
- **OpenAI-family** (copilot serving GPT, and any gateway routing to an
  OpenAI model) caches a stable prefix implicitly, with no request-side
  signal at all. `none` removes markers that path never needed; the provider
  keeps caching and keeps discounting. Expect cache reads with caching
  "off", and do not write a test that asserts otherwise — an implicit-caching
  provider will fail it by working correctly.

So `none` is "send no directives", which is all a client can promise.

Retention is a `ttl` field, not a type — `1h` reaches the wire as
`{"type":"ephemeral","ttl":"1h"}`. Neither TTL needs a beta header. 1h costs
2x base input on the write against 1.25x for 5m, so it pays only across a
session long enough to have re-written the short cache more than twice.

`system.cache_markers` selects *how* a route is marked, independent of
whether caching is on:

```
figaro set system.cache_markers blocks     # per-block cache_control
figaro set system.cache_markers top-level  # one request-level directive
figaro set system.cache_markers none       # mark nothing
```

The default, `auto`, asks the route. Direct Anthropic takes per-block
markers. A gateway that advertises a request-level directive gets that
instead, and places the breakpoints itself — which is the only correct
answer when the endpoint is a *router*, because the model is chosen after
the request arrives and both the minimum cacheable size and the breakpoint
budget are per-model.

Manual per-entry breakpoints still work and layer on top of the automatic
ones (mind the four-breakpoint ceiling):

```
figaro set system.tags[<LT>].cache_control ephemeral
figaro unset system.tags[<LT>].cache_control
```

where `<LT>` is a logical time in your own aria. Pick **stable prefixes** (the
credo, settled tool outputs) — never the current turn — and place them roughly
monotonically.

## Gateways and routes

A provider's endpoint is a `provider.Route`: base URL, dialect, auth, and a
`CacheCaps` descriptor saying what that endpoint actually honours. Point one
somewhere else with `ANTHROPIC_BASE_URL` (or the per-provider equivalent).

Capabilities are consulted, never assumed. A marker sent where it is not
understood is not harmless: some gateways hard-fail on it, and Anthropic's
own OpenAI-compatible shim drops it silently while still billing full input
— caching that looks enabled and is not. A route of unknown provenance is
marked with nothing.

Routes that support sticky routing also carry a session key derived from the
aria id (`session_id` in the body, `x-session-id` as a header,
`prompt_cache_key` as the fallback name). It keeps a multi-provider gateway
pinned to the endpoint holding the warm cache. It is a hash, it maps to
nothing, and it never enters telemetry.

## Future: fork-aware retention

Planned, not built: rather than one flat policy, score each span of nodes for
cache eligibility and promote hot, many-branch spans (high descendant count in
a conversation fork graph) to long retention. The decision is funnelled
through `resolveCacheControl` so a provider-implemented, memoized scorer can
slot in once the IR carries a fork graph. The fourth breakpoint is reserved
for it.

## Caching and the context figure

Because the whole prompt is normally a cache *read*, the context size shown by
`figaro status` / `figaro list` must sum all three input buckets plus the
turn's output:

```
InputTokens + CacheReadTokens + CacheWriteTokens + OutputTokens
```

`tokens.ContextFromUsage` is the one definition; both the full fold
(`tokens.ContextSize`) and the agent's incremental fast path
(`Agent.refreshMetrics`) go through it, and `TestRefreshMetricsIncrementalMatchesFullFold`
keeps them honest. Summing only Input+Output — as figaro did before — reports
a cached aria as a few thousand tokens when it is really a few hundred
thousand.
