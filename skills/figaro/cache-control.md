# Cache control

Anthropic's prompt cache lets up to **four** `cache_control` breakpoints mark
cached prefixes; everything before a mark is reused on later turns at a
fraction of the input-token cost. Figaro applies this **automatically** — you
rarely need to touch it.

## What happens by default

Caching is **on by default at short (5m) ephemeral retention**. Each turn,
`markCacheBreakpoints` stamps three breakpoints:

- the last **system** block (the credo/identity prefix),
- the last **tool**,
- the leaf of the **last input message** (the rolling tail).

So the static prefix is a cache *read* every turn after the first, and the
rolling breakpoint caches the growing transcript so the next turn reads all
prior history. That leaves one of the four breakpoints free. Confirm it's
working from a `FIGARO_WIRE_DIR` dump: a follow-up turn shows
`cache_read_input_tokens > 0`.

## Overriding

`system.cache_control` on the chalkboard overrides the default:

```
figaro set system.cache_control none     # disable caching entirely
figaro set system.cache_control 1h        # force long (1h) retention*
figaro set system.cache_control ephemeral # explicit short (the default)
```

\*1h additionally needs the `extended-cache-ttl` beta, which figaro does not
send yet — treat 1h as future-facing for now.

Manual per-entry breakpoints still work and layer on top of the automatic
ones (mind the four-breakpoint ceiling):

```
figaro set system.tags[<LT>].cache_control ephemeral
figaro unset system.tags[<LT>].cache_control
```

where `<LT>` is a logical time in your own aria. Pick **stable prefixes** (the
credo, settled tool outputs) — never the current turn — and place them roughly
monotonically.

## Future: fork-aware retention

Planned, not built: rather than one flat policy, score each span of nodes for
cache eligibility and promote hot, many-branch spans (high descendant count in
a conversation fork graph) to long retention. The decision is funnelled
through `resolveCacheControl` so a provider-implemented, memoized scorer can
slot in once the IR carries a fork graph. The fourth breakpoint is reserved
for it.
