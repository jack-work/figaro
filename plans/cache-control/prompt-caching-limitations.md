# Prompt Caching on the Anthropic API — Limitations on the Claude Code OAuth Path

> Personal reference document. Captures why client-controlled `cache_control` does not engage on the OAuth (Claude subscription) auth path, what the underlying policy is, how to detect and monitor changes, and what options exist if caching becomes a real need. **No implementation work is intended from this document** — it is a write-up for future-self.

Last updated: 2026-04-29.

---

## Summary

Figaro's Stage 0.5 wiring of `cache_control: ephemeral` on the system block, the last tool definition, and the leaf-1 message is functionally correct. Unit tests confirm byte-stable prefix construction and correct `cache_control` placement. End-to-end against a real Anthropic endpoint, however, every request returned:

```json
"usage": {
  "input_tokens": 2240,
  "cache_creation_input_tokens": 0,
  "cache_read_input_tokens": 0,
  "cache_creation": {
    "ephemeral_5m_input_tokens": 0,
    "ephemeral_1h_input_tokens": 0
  }
}
```

The directive is parsed (the structured `cache_creation` object appears in the response), but Anthropic allocates **zero** tokens to the cache. This is not a bug to be fixed client-side. It is a structural policy:

> **Prompt caching is treated as an API-tier feature. It is not enabled for OAuth-authenticated requests originating from third-party clients, regardless of the `cache_control` markers in the request body.**

The figaro setup hits this policy on the OAuth + `claude-code-20250219` beta path. The wiring will start engaging the cache the moment the auth path supports it (e.g. moving to `ANTHROPIC_API_KEY`), but no client-side change makes caching engage on the current OAuth path.

---

## Empirical observation

What was tested:

- Two consecutive turns to the same persistent figaro within a few seconds (well under the 5-minute TTL).
- Identical conversation prefix across turns (modulo the new user message at the leaf).
- `cache_control: {type: "ephemeral"}` correctly placed on the last system block, the last tool definition, and the second-to-last message (verified by dumping the request JSON via a temporary debug log).
- OAuth Bearer auth using a Claude Pro/Max subscription token.
- `anthropic-beta: claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14`.
- Adding `prompt-caching-2024-07-31` to the beta header was tested and made no difference.

What Anthropic returned:

- `cache_creation_input_tokens: 0` and `cache_read_input_tokens: 0` on every request.
- The structured `cache_creation` block (`ephemeral_5m_input_tokens`, `ephemeral_1h_input_tokens`) was present and reported zero.
- HTTP 200, no error body.

Two distinct behaviors are documented in the wider community:

1. **Silent-zero (pre-2026-03-17)** — what figaro observed. The directive is parsed, the structured field appears in the response, but no caching happens.
2. **Hard-reject (post-2026-03-17)** — HTTP 400 with `{"type":"error","error":{"type":"invalid_request_error","message":"Error"}}`. OpenCode's [issue #17910][opencode-17910] documents the exact transition date.

Whether a given OAuth token sees behavior (1) or (2) appears to depend on the exact endpoint and beta header combination. Some paths still silent-zero; others now hard-reject. The fix from a client perspective is the same in both cases: don't send `cache_control` on OAuth-authenticated requests.

---

## Why the policy exists

Caching on Anthropic's side has real cost — KV-cache memory provisioning, routing stickiness, the 24h offload tier — and that cost is recovered through the API tier's input-token billing. Subscription users (Claude Pro/Max, ~$20–$200/mo flat) pay for first-party Claude Code usage *specifically*. If third-party clients could attach `cache_control` to OAuth requests and trigger arbitrary cache writes, any developer could route arbitrary API workload through a $20/month subscription and get caching at zero marginal cost. The policy is structural revenue protection, not a configuration mistake.

Two related boundary tightenings reinforce this read:

- **2026-03-17.** OAuth + `cache_control` started returning HTTP 400 (was silent-zero before). Multiple downstream tools broke simultaneously without changing their code.
- **2026-04-04 (~12:00 PM PT).** Anthropic broadly blocked Claude Pro/Max subscription access for third-party harnesses (OpenCode, OpenClaw, etc.). This was a separate, broader access-tier change but is part of the same policy direction. See [Anthropic kills Claude subscription access for third-party tools][devto-anthropic-kills] (DEV Community).

The community read is consistent: OAuth subscription tokens carry caching entitlements, but the entitlements are scoped to traffic that **looks exactly like Claude Code** — same beta headers, billing header signature, system-prompt layout, tool naming convention, and request shape. Replicating any subset is insufficient.

---

## What does engage caching

### Authenticated by API key

`ANTHROPIC_API_KEY` from the [Anthropic Console][anthropic-console] is the canonical, supported, documented path. Caching engages with no special headers when the request meets:

- Sufficient prefix length (see model minimums below).
- A `cache_control` breakpoint placed on a content block within the prefix.
- The marked prefix matches byte-identically across requests within the TTL window.

### First-party Claude Code traffic

Claude Code itself gets caching on OAuth, and on Max subscriptions specifically gets a 1-hour TTL via a server-side feature flag (`tengu_prompt_cache_1h_config`) keyed off subscription tier. This is documented circumstantially in [Anthropic API source-leak analysis][alex000kim-source-leak]. The relevant point for our purposes: replicating Claude Code's request shape exactly (so server-side validation classifies the request as first-party) would also engage caching, but doing so puts a third-party client deep into ToS territory and chases a moving target — Anthropic has rotated the validation surface multiple times since January 2026.

### Automatic caching (no `cache_control` needed)

Anthropic added an automatic-caching mode in early 2026: setting a top-level `cache_control` parameter on `messages.create` (rather than on individual blocks) lets the API pick **one** breakpoint heuristically. Useful as a smoke test, less useful for an agent loop with multiple cacheable regions (system / tools / messages-up-to-leaf). Per [Anthropic's docs][anthropic-prompt-caching]: "Caching only kicks in when the cached prefix exceeds a per-model minimum."

It is not currently known whether automatic caching engages on OAuth paths where explicit `cache_control` is gated off. The `cache_creation_input_tokens` field reports the same way, so the easiest test is: set up an API-key path side-by-side with the OAuth path, run the same prompt, observe whether the OAuth path's cache numbers move at all.

---

## Per-model minimums for cache writes

Even if the auth path supports caching, a request whose marked prefix is below the model's cache minimum is silently dropped (no error, no cache write).

| Model | Minimum tokens to cache |
|---|---:|
| Mythos Preview, Opus 4.7, Opus 4.6, Opus 4.5 | 4,096 |
| Sonnet 4.6 | 2,048 |
| Sonnet 4.5, Opus 4.1, Opus 4, Sonnet 4, Sonnet 3.7 | 1,024 |
| Haiku 4.5 | 4,096 |
| Haiku 3.5 | 2,048 |

Source: [Anthropic prompt-caching docs][anthropic-prompt-caching].

**Relevance to figaro's smoke test.** The two-turn test against `claude-opus-4-6` had `input_tokens: 2240` on turn 1 and `2261` on turn 2. **Both are below the 4,096 minimum for Opus 4.6.** Even on an API-key path with everything wired correctly, caching would not have engaged on those single-turn prompts. The OAuth gating is the first-order issue, but cache minimums are an independent concern: a fresh chalkboard test on Opus 4.6 needs a multi-turn conversation that exceeds 4,096 tokens of prefix (typically 3–5 turns, depending on tool-use density) before caching becomes observable.

---

## Pricing reference

| Operation | Multiplier vs. base input |
|---|---:|
| Cache write (5m TTL) | 1.25× |
| Cache write (1h TTL) | 2.0× |
| Cache read (hit) | 0.1× |

So for `claude-opus-4-6` at $5/MTok base input:
- 5m write: $6.25/MTok
- 1h write: $10/MTok
- Read: $0.50/MTok

The cache read discount (10× cheaper) is the headline; on a long, prefix-stable conversation, the savings dominate the small write surcharge after one or two turns.

Source: [Anthropic prompt-caching docs][anthropic-prompt-caching].

---

## Approaches if caching becomes a real need

Three options, ranked by recommended-ness for figaro's situation:

### 1. Switch to API-key auth (canonical)

Set `ANTHROPIC_API_KEY` in env or `~/.config/figaro/providers/anthropic/config.toml`. Figaro's existing auth resolver checks both before falling back to OAuth. The wiring already projected by Stage 0.5 is correct for the API-key path; this is a configuration flip, not a code change.

Tradeoff: API-key billing is per-token rather than the flat subscription rate. Whether this is cheaper or more expensive depends on volume — the [OpenClaw + Claude Code Costs 2026 writeup][shareuhack-costs] is the cleanest comparison. For a developer-grade workload, the API-key path is usually price-competitive with Pro and well below Max once caching engages.

### 2. Detect OAuth at the provider layer and strip `cache_control` (defensive)

OpenCode shipped this fix (then closed [#17910][opencode-17910] as "not planned" — they backed out the PR). The shape: when `isOAuthToken(apiKey)` is true, skip `markCacheBreakpoints` entirely. This avoids the HTTP 400 error if the policy tightens further on figaro's auth path, costs nothing in functionality (caching wasn't engaging anyway), and keeps the test suite green.

Worth doing as a small defensive fix in `internal/provider/anthropic/anthropic.go` even if API key is the primary intended path, since some users will configure OAuth.

### 3. Replicate Claude Code's request shape (NOT recommended)

The community has documented the validation surface in considerable detail. Concrete operational references:

- [griffinmartin/opencode-claude-auth][griffin-opencode-claude-auth] — TypeScript reference, tracked by Anthropic's anti-spoofing rotations. Each rotation event is documented in the README.
- [kristianvast/hermes-claude-auth][kristian-hermes] — Python sitecustomize hook tracking the above; the diff doubles as a spec of what the server checks for.
- [zacdcook/openclaw-billing-proxy][zac-billing-proxy] — different angle, with the most detailed write-up of what the server actually inspects: the `x-anthropic-billing-header` (84-character signature), a body classifier scanning for known third-party phrases, and a tool-name signature detector flagging requests whose tool set looks like a known harness. Striking empirical finding: identical schemas with PascalCase Claude-Code-style names (Bash, SendMessage, ContextGrep) pass the gate; the same schemas with original third-party names get flagged at ~2.8KB body size.
- [changjonathanc/anthropic_oauth.py gist][chang-oauth-py] — minimal OAuth-flow demo (predates the 2026-04-04 validation tightening).

What the request shape requires, synthesized from those sources:

1. User-Agent matching the official CLI (`claude-cli/<version>`).
2. The `claude-code-20250219` beta header, possibly with companion betas (`prompt-caching-scope-2026-01-05`, `advanced-tool-use-2025-11-20`).
3. `x-anthropic-billing-header` computed via a salted signature scheme that has been rotated multiple times since January 2026.
4. System prompt that begins with the canonical Claude Code identity block (`"You are Claude Code, Anthropic's official CLI for Claude."`) and whose non-identity content has been moved to the first user message as `<system-reminder>` blocks. The leaked system prompt itself is at [Piebald-AI/claude-code-system-prompts][piebald-prompts] and [asgeirtj/system_prompts_leaks][asgeirtj-leaks].
5. Tool definitions using PascalCase naming, ideally matching Claude Code's canonical tool set rather than custom names.
6. For Opus 4.6 with adaptive thinking, no `temperature` parameter (including it triggers HTTP 400; the official client omits it).
7. Avoidance of strings the body classifier looks for — product names, internal tool names, distinctive phrases (`OpenClaw`, `sessions_spawn`, `HEARTBEAT_OK`, `clawhub`, etc.).

**Why this isn't recommended for figaro:**

- **ToS exposure.** Anthropic's recent moves (the March policy tightening, the April 4 third-party block) signal active enforcement. Spoofing first-party traffic to access subscription-tier caching is the kind of behavior the policy is designed to detect and shut down.
- **Moving target.** The validation surface has rotated multiple times. Tracking it requires ongoing maintenance work that doesn't compound.
- **Identity contamination.** The required system-prompt structure forces the agent to identify as "Claude Code" — incompatible with figaro's `credo.md` persona work.
- **Background-policy risk.** Even if implemented carefully, the next rotation could brick the auth path silently. The legitimate alternatives (API key, or the existing OAuth path used as-is without caching) don't share this risk.

The upstream community write-ups for the policy/legal backdrop are [Anthropic's Walled Garden: The Claude Code Crackdown][paddo-walled-garden] and [the source-leak architecture analysis][alex000kim-source-leak]. Worth reading once, in case useful context emerges before any decision.

---

## Monitoring

Two cheap signals for detecting if/when this changes:

### Local: `figaro list` CACHE column

Already wired in Stage 0.5. Shows cumulative `cache_read_tokens / cache_write_tokens` per aria. Currently always `-` (zero/zero) on the OAuth path. The day a value becomes non-zero is the day either:

- A configuration change moved figaro to an API-key path, or
- Anthropic's policy on this OAuth path changed.

Run a multi-turn prompt sequence (>4,096-token total prefix on Opus 4.6) every quarter or so and check the column.

### Upstream: GitHub issue trackers

The downstream third-party-client community is the best early-warning system for policy shifts. Three repositories worth watching:

- [anomalyco/opencode][opencode-repo] — the most active downstream client. Issue #17910 was the canary for the March 17 tightening. Most reliable bellwether.
- [griffinmartin/opencode-claude-auth][griffin-opencode-claude-auth] — README's "rotation events" section is the closest thing to a public changelog of Anthropic's anti-spoofing fingerprint moves.
- [openclaw/openclaw][openclaw-repo] — separate ecosystem, complementary signal. They were also affected by the April 4 access-tier change.

Anthropic's own [API changelog][anthropic-changelog] is the authoritative source but lags the community signals by days-to-weeks.

### Internal: per-aria event logs

`~/.local/state/figaro/figaros/<id>.jsonl` carries each `stream.message` notification with the full IR `Usage` struct. A grep across these logs for any `"cache_read_tokens":[1-9]` or `"cache_write_tokens":[1-9]` would surface the first cache hit if it ever happens. Useful as a passive monitor.

---

## Why the chalkboard work is still valuable

Even if explicit caching never engages on this auth path:

1. **Automatic caching may engage where explicit doesn't.** Anthropic now does some prefix caching opaquely. The byte-stable prefix that figaro's chalkboard work enforces is exactly what automatic caching also rewards. There's no way to verify this from response metadata (the `cache_creation_input_tokens` field reports the same regardless of source), but the discipline costs nothing.
2. **API-key path is one config flip away.** Whenever figaro is moved to API-key auth — for production use, for a CI pipeline, for a different account — the existing wiring engages immediately. No code change needed.
3. **Future providers.** OpenAI's caching, AWS Bedrock's, Google Vertex's all reward prefix stability. The chalkboard's separation of "stable identity content" from "volatile per-turn state" is a cross-provider win.
4. **The negative architectural outcomes are also wins:** the credo no longer churns hourly, panic recovery preserves identity, the `staticScribe` injection pattern is gone, state lives in one named place (the chalkboard) with a clear lifecycle. These have value independent of whether the cache numbers ever move.

The benchmarks confirm the work is cheap: ~15 µs of overhead per turn (Snapshot.Diff + Render + applyRenderer) vs. hundreds of milliseconds for the API round-trip. Negligible cost regardless of the cache outcome.

---

## References

### Anthropic primary sources

- [Prompt caching — Claude API Docs][anthropic-prompt-caching] — pricing multipliers, per-model minimums, TTL options, basic mechanics.
- [Pricing — Claude API Docs][anthropic-pricing] — base input/output rates per model.
- [API changelog][anthropic-changelog] — official source for breaking-change announcements; lags community by days-to-weeks.
- [Anthropic Console][anthropic-console] — where to provision API keys for the supported caching path.

### Downstream-client incident reports (ground-truth for policy shifts)

- [opencode#17910 — OAuth + cache_control HTTP 400 since 2026-03-17][opencode-17910] — the canary for the March policy tightening. Closed as "not planned" via PR #18311.
- [Anthropic kills Claude subscription access for third-party tools like OpenClaw — DEV Community][devto-anthropic-kills] — context for the April 4 access-tier change.
- [OpenCode Blocked by Anthropic? 3 Working Solutions (2026 Guide) — NxCode][nxcode-blocked] — practical workarounds writeup.
- [Anthropic's Walled Garden: The Claude Code Crackdown — paddo.dev][paddo-walled-garden] — policy analysis.
- [How to Fix Claude Code API Error 400 After Upgrading (system.2.cache_control Issue) — Medium][medium-system2-fix] — fix recipe (strip cache_control on OAuth).
- [OpenClaw + Claude Code Costs 2026 — shareuhack][shareuhack-costs] — API key vs Pro vs Max economics, post-cutoff.

### Replication references (for understanding the validation surface, not for use)

- [griffinmartin/opencode-claude-auth][griffin-opencode-claude-auth] — TypeScript reference; tracks rotations.
- [kristianvast/hermes-claude-auth][kristian-hermes] — Python sitecustomize patcher; spec by example.
- [zacdcook/openclaw-billing-proxy][zac-billing-proxy] — best documentation of server-side validation specifics.
- [changjonathanc/anthropic_oauth.py][chang-oauth-py] — minimal OAuth flow demo (pre-2026-04-04).
- [Claude Code source leak: architecture analysis — alex000kim.com][alex000kim-source-leak] — feature-flag and validation-mechanism cataloging.

### Leaked first-party prompts (reference for what server-side classifiers look for)

- [Piebald-AI/claude-code-system-prompts][piebald-prompts] — full catalog of Claude Code's system reminders and identity prompts.
- [asgeirtj/system_prompts_leaks (Anthropic/claude-code.md)][asgeirtj-leaks] — alternate source.

### Tooling for measurement and explicit caching done right (API-key path)

- [How to Add Prompt Caching to an Anthropic SDK App and Measure the Hit Rate — Start Debugging][startdebugging-howto] — measurement methodology for an API-key setup.
- [Cut Anthropic API Costs 90% with Prompt Caching 2026 — Markaicode][markaicode-cut-costs] — practical guide.
- [Claude Prompt Caching in 2026: The 5-Minute TTL Change — DEV Community][whoffagents-ttl] — context on the early-2026 default-TTL drop from 60min to 5min.

[anthropic-prompt-caching]: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
[anthropic-pricing]: https://platform.claude.com/docs/en/about-claude/pricing
[anthropic-changelog]: https://platform.claude.com/docs/en/build-with-claude/api-changelog
[anthropic-console]: https://console.anthropic.com/
[opencode-17910]: https://github.com/anomalyco/opencode/issues/17910
[opencode-repo]: https://github.com/anomalyco/opencode
[devto-anthropic-kills]: https://dev.to/mcrolly/anthropic-kills-claude-subscription-access-for-third-party-tools-like-openclaw-what-it-means-for-3ipc
[nxcode-blocked]: https://www.nxcode.io/resources/news/opencode-blocked-anthropic-2026
[paddo-walled-garden]: https://paddo.dev/blog/anthropic-walled-garden-crackdown/
[medium-system2-fix]: https://medium.com/codex/how-to-fix-claude-code-api-error-400-after-upgrading-system-2-cache-control-issue-a490f7d343e0
[shareuhack-costs]: https://www.shareuhack.com/en/posts/openclaw-claude-code-oauth-cost
[griffin-opencode-claude-auth]: https://github.com/griffinmartin/opencode-claude-auth
[kristian-hermes]: https://github.com/kristianvast/hermes-claude-auth
[zac-billing-proxy]: https://github.com/zacdcook/openclaw-billing-proxy
[chang-oauth-py]: https://gist.github.com/changjonathanc/anthropic_oauth.py
[alex000kim-source-leak]: https://alex000kim.com/posts/2026-03-31-claude-code-source-leak/
[piebald-prompts]: https://github.com/Piebald-AI/claude-code-system-prompts
[asgeirtj-leaks]: https://github.com/asgeirtj/system_prompts_leaks
[openclaw-repo]: https://github.com/openclaw/openclaw
[startdebugging-howto]: https://startdebugging.net/2026/04/how-to-add-prompt-caching-to-an-anthropic-sdk-app-and-measure-the-hit-rate/
[markaicode-cut-costs]: https://markaicode.com/anthropic-prompt-caching-reduce-api-costs/
[whoffagents-ttl]: https://dev.to/whoffagents/claude-prompt-caching-in-2026-the-5-minute-ttl-change-thats-costing-you-money-4363
