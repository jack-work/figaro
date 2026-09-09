# The Anthropic OAuth posture, and the exit

Status: standing. Revisit when a refresh starts failing or a policy notice
arrives.

## How figaro talks to Anthropic today

The anthropic providers present the **Claude Code OAuth credential** (a
claude.ai subscription) and identify as Claude Code itself:
`User-Agent: claude-cli/<version>`, the `claude-code-20250219` beta header,
and a claimed client version (`anthropicmodels.ClaudeCodeVersion`). Token
mint and refresh live in **hush**, not here.

This works precisely because the requests are indistinguishable from the
real client. It is subscription-priced inference for an agent harness, and
it is the arrangement this document is honest about.

## What the ground looks like (September 2026)

Three facts, found the day Fable 5.1 started refusing us:

1. **Models are gated on the claimed client version.** Fable 5.1 returns
   400 `claude_code_version_too_old` below 2.1.251. The check is a numeric
   threshold, not a whitelist of real releases — and the floor sits *above*
   the stable channel, so even genuine Claude Code on stable fails
   (anthropics/claude-code#91345). Hence the rule on the constant: track
   `dist-tags.latest`.

2. **The policy line is now explicit.** The Claude Code docs were updated
   to ban OAuth token usage in third-party tools; `claude setup-token`
   credentials (`sk-ant-oat01-*`) are rejected by the Messages API
   (claude-code#28091, #37205). What figaro does is not a gray area in the
   text; it is a gray area only in enforcement.

3. **Enforcement is visibly tightening.** Since ~June 2026 the token
   endpoint (`/v1/oauth/token`) refuses `claude-cli/*` User-Agents outright
   (hermes-agent#48534) — an anti-abuse net aimed at exactly this pattern.
   That is the *refresh* path, which is hush's; figaro's turns work today
   because hush's refresh passes today.

## The decision

**If OAuth becomes non-viable, figaro changes providers. That is the whole
contingency.** No countermeasures, no UA arms race: the moment the
arrangement stops working — refreshes blocked, version gates that cannot be
cleared, account action — we switch credentials, not disguises.

The exit is already built and deliberately boring:

- **A plain API key**, via `api_key_file` (`auth.FileValue`): first in the
  resolution chain, no impersonation anywhere on the path, and the shape a
  server deployment uses anyway (sops → `/run/secrets` → path). Metered
  pricing instead of subscription; that is the cost of the exit, and it is
  a price question rather than an architecture question.
- **Copilot**, already a first-class provider, carrying most of the same
  models under its own sanctioned agent quota.
- **Any OpenAI-compatible endpoint** through the openaichat provider.

## What to watch, and what each sign means

| sign | meaning | response |
|---|---|---|
| 400 `claude_code_version_too_old` | routine version-floor bump | bump `ClaudeCodeVersion` to npm `latest` |
| refreshes failing / 429 from `/v1/oauth/token` | the UA net caught the refresh path | check hush's UA on that endpoint; if intentional targeting, take the exit |
| 401s with a valid, fresh token | credential class revoked | take the exit |
| policy notice on the account | the question has been asked directly | take the exit |

The first row is maintenance. The other three are the same instruction.
