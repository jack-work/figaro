# Google AI API R&D, `v0.70` Audit Cleanup, and Issues `#113`..`#120`

Date: 2026-11-16. Author: Figaro Engineer IX (`947bf85d`).

## Executive Summary

1. **`v0.70` / `agy` Audit & Cleanup (`lane/agy-gemini-audit`, `a92ef1fc1`)**:
   - Audited every file added or modified across `ab76ce3cd..27b36ef37` (`7f542d3d4`, `c39959891`, `c214354e4`, `a11b57a96`, `64bddc28e`, `5cdffa4d3`).
   - Retired `internal/provider/figir` (`gemgate` sidecar dialect; zero live fleet or config references) for a net reduction of **1,460 lines**, keeping `message.Content.Signature` for Gemini `thoughtSignature` round-tripping and fixing `scripts/boundary-allow.txt` (`figaro-boundary`).
   - Fixed six sharp edges across `gemini`, `upstream`, `catalogmaps`, `auth`, and `routed` (detailed in Section 1), stripped production comments to house standard, and fixed `#118`.
2. **Google AI R&D & Unified Scope Quota (Sections 2 and 3)**:
   - Mapped `thinkingConfig` (`includeThoughts: true`, `thinkingLevel` on Gemini 3/4 vs `thinkingBudget` on Gemini 2.5) and fixed the two-part root cause of 90-second cold-turn stalls (`upstream.WithInitialStall` honoring `system.first_byte_timeout` + streaming thought summaries).
   - Fixed missing `window` (`null`) and wrong `max_output` (`1,048,576`) across all Gemini and Claude Code Assist models in `figaro models`.
   - Documented Google's native **Decisions API** (`POST /v1beta/decisions` with `gemini-decision-1`: parallel calibrated `noul`/`choice`/`score` probabilities in one pass with zero output tokens) as a direct backend for Figaro `decide` and `#109`.
   - Designed unified Gemini quota & credit visibility for Scope (`figscope` + `rou2` read facade) across Cloud Code Assist (`v1internal:retrieveUserQuota`, `loadCodeAssist`, and passive per-turn `consumedCredits`/`remainingCredits`) and standard API keys (GCP `serviceruntime` quota metrics + OTLP span derivation).
3. **Issues `#113`..`#120` Triage & Delivery (Section 4)**:
   - **Built & gated**: `#113`, `#116`, `#117` (with `#110`), `#118`, `#119`, and `#120` (following `hush v0.15.0` merge at `73496746`).
   - **Design calls isolated for Gluck**: `#115` (cold-start restore storm: 3-part low-risk surgical fix vs form-delta checkpointing) and `#114` (fork `--replay` mode: 3 concrete boundary choices).

---

## 1. Audit of `ab76ce3cd..27b36ef37` (`agy` / `cymbal-lunchbox`)

Branch `lane/agy-gemini-audit` (`27b36ef37..a92ef1fc1`, 5 atomic commits, `gofmt` + `check-boundary.sh` + `go vet` + `go test -race` green):

| Commit | Scope | Defects Fixed & Cleanup |
|---|---|---|
| `c9bfbdde3` | `internal/provider/figir`, `routed`, `relay`, `api/message` | Verified zero callers of `fig-ir` or `gemgate` in `cheroot-config`, `spain-flake`, and outfits. Deleted `internal/provider/figir/*` (-1,460 net lines) and `Content.Meta`; kept `Content.Signature` for Gemini `thoughtSignature`; added `cmd/figaro -> internal/provider/gemini` to `scripts/boundary-allow.txt` (broken in `5cdffa4d3`). |
| `bcfaf0469` | `internal/provider/gemini` | Enabled `IncludeThoughts: true` on reasoning models; replayed signed `ContentThinking` parts so signatures attached to thought blocks survive multi-step turns; stamped `skip_thought_signature_validator` only on the first unsigned `functionCall` in a parallel batch (bumping translator fingerprint to `/v2`); replaced single-line `bufio.Scanner` with 32 MiB multi-line `bufio.Reader` SSE framing; broke recursive `$ref` cycles in `schema.go` (`seenRefs`), stopped forcing empty `"properties": {}` onto nested map objects, and filtered non-string `enum` values; capped non-200 error reads at 4 KiB and redacted `Bearer`, `ya29.*`, and `AIza*` tokens. |
| `53b696b3b` | `internal/auth`, `methods`, `routed` | Enforced HTTPS `token_url` (no userinfo/query/fragment) and safe credential names in `confidential.go`; switched state writes to `os.CreateTemp` (`0600`); bounded `client_secret_file` reads to 64 KiB and zeroed buffers; fixed PKCE pasted callback URL state bypass (requiring an exact `state` match whenever a URL is pasted); routed `auth.Login` paste flow through `oauthClient` with redacted errors; kept Gemini/CodeAssist XWAL cache across `thinking_effort`/`thinking_budget` changes; stripped all production code comments. |
| `7d22d9525` | `api/models`, `modelcat`, `routed`, `daemon`, `cli` | **Fixes `#118`**: tracked `CatalogOff` explicitly so `[routing] catalog = "off"` displays `catalog off` without network calls or `failed` status while keeping configured/wanted models; added `Registration.Configured` so unconfigured `gateway` and `azure` presets are neither polled nor shown as failed. |
| `a92ef1fc1` | `internal/upstream`, `provider`, `gemini`, `catalogmaps` | Extended `upstream.Transport` pre-body stall window via `WithInitialStall` inside `WatchFirstByte` so `system.first_byte_timeout = "5m"` is honored before first body byte while keeping the 90s inter-chunk watchdog afterward; fixed `v1internal` `maxTokens` -> `context` window mapping and `gemini.toml` globs; mapped `system.thinking_effort` to `ThinkingLevel` / `ThinkingBudget`. |

---

## 2. Google AI API R&D (`v1beta` & Cloud Code Private `v1internal`)

### 2.1 Thinking Summaries, `thinkingLevel`, and the 90s Stall

- **Why turns >250k stalled at 1m30s on `v0.70.1`**:
  1. `internal/provider/gemini/wire.go` omitted `thinkingConfig` whenever `system.thinking_budget` was unset (`includeThoughts: false`). On large contexts, `gemini-4-carbon` reasoned silently for >90s and buffered full `functionCall` JSON without emitting SSE frames.
  2. `internal/upstream/transport.go` enforced a fixed `Defaults.Stall = 90s` watchdog independent of `system.first_byte_timeout`.
  - Both halves are now fixed in `bcfaf0469` and `a92ef1fc1`.
- **`thinkingLevel` vs `thinkingBudget` model matrix** (verified against `aistudio.google.com/docs/thinking.md` and `whats-new-gemini-4-carbon.md`):
  - Sending both `thinkingLevel` and `thinkingBudget` returns HTTP `400`; `thinkingBudget` is deprecated on Gemini 4, while Gemini 2.5 rejects `thinkingLevel`.
  - `gemini-4-carbon`, `gemini-4-argon`, `gemini-3.8-flash`: `low`, `medium` (default), `high` (cannot disable thinking; `minimal` unsupported).
  - `gemini-4-hydrogen`: `low`, `medium`, `high` (default), `xhigh` (cannot disable thinking).
  - `gemini-3.6-flash`, `gemini-3.5-flash-lite`, `gemini-3.1-flash-lite`: `minimal`, `low`, `medium`, `high`.
  - `gemini-2.5-pro` (`128..32768`, `-1` dynamic), `gemini-2.5-flash` (`0..24576`), `gemini-2.5-flash-lite` (`0` or `512..24576`): integer `thinkingBudget`.
  - Note on `max_output_tokens`: on Gemini, `maxOutputTokens` caps `thoughts + visible output` combined (`65,536` streaming hop ceiling; `1,048,576` unified context window). Lower `thinkingLevel` rather than shrinking `maxOutputTokens` when curbing spend.

### 2.2 Missing `window` and Wrong `max_output` in `figaro models`

Live inspection of `figaro models -j` on `gluck` exposed two stacked bugs in `5cdffa4d3`:

1. **`v1internal:fetchAvailableModels` field semantics (`internal/provider/gemini/catalog.go:68`)**:
   - Cloud Code Private returns `"maxTokens": 1048576` (Gemini 2.5/3/4), `1000000` (Claude 5.5), `131072` (GPT-OSS-120B), and omits `maxInputTokens` (`0`).
   - `normalizeCodeAssistModels` mapped `entry.MaxTokens` into `MaxOutputTokens` and `entry.MaxInputTokens` (`0`) into `MaxInputTokens`, leaving `context: null` on every model (disabling context % gauges and 60%/70% succession policies on Gemini seats) while setting `max_output: 1048576`.
2. **`internal/catalogmaps/gemini.toml` glob bug**:
   - `catalog.Reader.factFor` matches open globs with `strings.HasPrefix(n, f.prefix + "-")` (compare `anthropic.toml`: `"claude-opus-5*"`).
   - `gemini.toml` used `"gemini-*"` and `"claude-*"`, producing `f.prefix = "gemini-"` and testing `"gemini--"` (double hyphen), so `[catalog.facts]` matched zero models.
- **Shipped fix (`a92ef1fc1`)**: when `maxInputTokens == 0` in `normalizeCodeAssistModels`, `maxTokens` populates `MaxInputTokens` (context window), and `"gemini*"` / `"claude*"` in `gemini.toml` supply `max_output = 65536` (`src.context = "api"`, `src.max_output = "mapping"`). Standard `GET /v1beta/models` already supplies both `inputTokenLimit` and `outputTokenLimit`.

### 2.3 High-Leverage Preview Capabilities for Figaro

1. **Native Decisions API (`POST /v1beta/decisions`, model `gemini-decision-1`)**:
   - Evaluates a map of typed questions (`noul` calibrated P(yes) in `0.01..0.99` + `confidence`, `choice` categorical probabilities, `score` ordered rubric probabilities) over shared text/PDF/image/audio/video input in **one parallel forward pass** with **`total_output_tokens: 0`**.
   - **Recommendation**: Add a `decisions` transport to `internal/decide` for `gemini-decision-1`. It directly answers `#109` (multi-question System One batching in a single request) with calibrated probabilities and zero decode latency.
2. **Implicit Context Caching & Code Assist `session_id`**:
   - Explicit `cachedContents` is unsupported on Gemini 4; implicit caching is automatic above 4,096 prompt tokens (full prior-turn cache hit when a turn appends >= 2,050 tokens).
   - `stream.go` (`usageToIR`) already subtracts `CachedContentTokenCount` from `PromptTokenCount` to preserve Figaro's exclusive `InputTokens` contract.
   - `VertexGenerateContentRequest` on `v1internal` accepts `"session_id"` inside the inner `request` JSON (`google-gemini/gemini-cli` `code_assist/converter.ts:48`) in addition to HTTP `x-session-id`, improving backend cache stickiness.
3. **Raw JSON Schema Tool Definitions (`parametersJsonSchema`)**:
   - `v1beta` `FunctionDeclaration.parametersJsonSchema` accepts standard JSON Schema 2020-12 (`$defs`, `anyOf`, `additionalProperties`) without lossy OpenAPI subset translation. Keep `NormalizeToolParameters` (hardened in `bcfaf0469`) for `v1internal` compatibility, and consider `parametersJsonSchema` once `v1internal` parity is confirmed.
4. **URL Context & Search Grounding (`url_context`, `google_search`)**:
   - Gemini 4 supports combining `{"url_context": {}}` and `{"google_search": {}}` alongside custom `functionDeclarations` in a single request, though free-tier search quotas are capped (5,000/month shared on Gemini 3+); keep opt-in per outfit rather than default.

---

## 3. Gemini Quota & Usage Derivation for Unified Scope (`figscope` + `rou2`)

Following `~/dev/figscope/docs/unified-scope.md` (`/figscope/` telemetry + `/rou2/api/accounts` passive account facade, zero synthetic inference probes, unknown = `null`):

### 3.1 Cloud Code Assist OAuth (`cloudcode-pa.googleapis.com/v1internal`)

Verified against `google-gemini/gemini-cli` (`packages/core/src/code_assist/{types,server,converter}.ts`), three passive signals exist:

1. **Per-model quota buckets (`retrieveUserQuota`)**:
   - `POST https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota`
   - Request: `{"project": "<cloudaicompanionProject>"}`
   - Response (`RetrieveUserQuotaResponse`):
     ```json
     {
       "buckets": [
         {
           "modelId": "gemini-4-carbon",
           "tokenType": "REQUESTS",
           "remainingFraction": 0.82,
           "remainingAmount": "410",
           "resetTime": "2026-11-17T00:00:00Z"
         }
       ]
     }
     ```
2. **Tier & Google One AI credit pool (`loadCodeAssist`)**:
   - `POST https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist` with `{"cloudaicompanionProject": "<proj>", "mode": "HEALTH_CHECK", "metadata": {"pluginType": "GEMINI"}}`
   - Returns `currentTier` (`free-tier` | `standard-tier` | `legacy-tier`) and `paidTier.availableCredits[]` (`{"creditType": "GOOGLE_ONE_AI", "creditAmount": "<int64-string>"}`).
3. **Zero-call per-turn credit telemetry (`CaGenerateContentResponse`)**:
   - Every `v1internal:streamGenerateContent` envelope can include top-level `consumedCredits` and `remainingCredits` beside `response`. Recording safe integer span attributes (`gen_ai.usage.credits_consumed`, `gen_ai.usage.credits_remaining`) in `gemini/stream.go` streams live credit burn into `figscope` with zero polling overhead.

### 3.2 Standard Gemini API Keys (`generativelanguage.googleapis.com`)

- **HTTP headers**: `200 OK` responses omit `x-ratelimit-remaining-*` headers. Quota details appear on `429 RESOURCE_EXHAUSTED` via Google RPC `QuotaFailure` (`quotaMetric`, `quotaId`, `quotaValue`) and `RetryInfo` (`retryDelay`).
- **GCP Cloud Monitoring (when project viewer credentials exist)**: query `monitoring.googleapis.com/v3/projects/{project}/timeSeries` on `serviceruntime.googleapis.com/quota/rate/net_usage` and `serviceruntime.googleapis.com/quota/limit` (`service = "generativelanguage.googleapis.com"`).
- **Local OTLP derivation in `figscope` (works for every API key with zero credentials)**:
  - Aggregate ended `figaro.round` spans where `provider = "gemini"` into rolling 60-second RPM/TPM and Pacific-midnight (`America/Los_Angeles`, when Google RPD resets) request/token counters, paired against optional configured tier ceilings (`tier1`/`tier2`/`tier3`) or last observed `429` `QuotaFailure` ceiling.
- **Immediate `figscope/prices.json` update needed**:
  - `~/dev/figscope/prices.json` currently lacks `gemini-4-carbon` ($2/$10 intro thru Nov 30, then $4/$20; cache read 10%), `gemini-4-argon` ($2/$10 intro), `gemini-4-hydrogen` ($1.50/$7.50), `gemini-3.8-flash`, `gemini-3.5-flash-lite` ($0.30/$2.50), and `gpt-6*`, causing Gemini 4 spend to display as `$0.00` in Scope.

### 3.3 Proposed Scope Integration Boundary

- **On `spain` (`rou2` passive account adapter)**: add a passive `code-assist` / `gcp-monitoring` adapter to `/rou2/api/accounts` alongside Anthropic, OpenAI subscription, and Copilot when Gemini routes through `rou2` / `hush cred`.
- **On `gluck` (direct local `gemini` provider)**: have `figaro` emit safe OTLP gauge points (`figaro.provider.quota.remaining_fraction`, `figaro.provider.credits.remaining`, labeled by `provider`, `model`, `reset_time`) from piggybacked `remainingCredits` + periodic (e.g. 5m when active) `retrieveUserQuota` on its existing `CodeAssist` resolver, and update `figscope/prices.json` so `figscope` renders both live cost and quota bars without `figscope` ever touching `hush` tokens.

---

## 4. Figaro Issues `#113`..`#120` Triage & Status

### Ready & Gated Now

| Issue | Branch & Commit | Summary |
|---|---|---|
| **`#113`** | `lane/issues-steady` `186e582ce` | `EntryLimit` and `ContextLimit` clamp to `entry.MaxPrompt` when smaller than the active tier window (unless `system.max_context_tokens` overrides), fixing understated context % on `gpt-6.1-sol` (922k ceiling vs 1.05M window). |
| **`#116`** | `lane/issues-steady` `89c03405b` | Distinguishes `!HasIdentity()` ("no vault yet: run `figaro provider login <name>` once in a shell to create it") from a sleeping `hush` in the pager, and points non-TTY service logins to `api_key_file`, `$CREDENTIALS_DIRECTORY`, and `[service_tokens]`. |
| **`#117` + `#110`** | `lane/issues-steady` `3a013d80e` | Stamps `Figaro-Node` (`[telemetry] node_label` or `Backend.NodeID()`) alongside `Figaro-Id` across turn rounds, inquiry-method decisions, explicit `decide` calls, and HTTP sandbox calls, overwriting forged caller headers. |
| **`#118`** | `lane/agy-gemini-audit` `7d22d9525` | Tracks `CatalogOff` so `[routing] catalog = "off"` prints `catalog off` (preserving configured/wanted models) and omits unconfigured `gateway`/`azure` presets from `@models`. |
| **`#119`** | `lane/issues-steady` `abbe190a5` | Allows lifetime/reap-only updates on `ErrFormSealed` nodes to mirror into `_meta/<id>.json` so `kill`, `set system.ttl`, and sweep reap tombstoned-form figaros; adds `figaro doctor tombstones [-j] [-n] [--reap]`. |
| **`#120`** | `lane/issue-120-cred` (gating) | Per-provider `[cred] socket = ... name = ...` in `boot.BuildResolver` speaking `hush cred serve`'s `GET /cred/<name>` protocol (`v0.15.0` merged at `73496746`), caching until expiry, re-fetching once on 401, mutually exclusive with `[oauth]`/`service_token`, and wired into `provider ls`/`login`/`logout`. |

### Needs Gluck's Design Call Before Building (`#115` and `#114`)

#### `#115`: Cold daemon start (`~780%` CPU, `aria.list` starved ~25 min)

Code audit identified four concrete causes:
1. **`labelOf` in `aria.list` (`internal/store/xwal_backend.go:845`)**: on cold boot, every fork-born node (`n.Stump == ""`) misses `b.labels` and calls `b.FormState(n.ID)` under `GOMAXPROCS` workers while acquiring `b.mu.Lock()` twice per node, even though `_meta/<id>.json` already mirrors `OutfitName` and `OutfitVersion`.
2. **`RefsNeedMigration` at boot (`internal/angelus/angelus.go:251`, `internal/store/libretto_reconcile.go:37`)**: replays every `@libretto::*` stump from LT 1 concurrently with `warmListing` on every restart, even when already migrated on a prior boot.
3. **Unbounded inline `restoreOne` (`internal/angelus/restore.go:29`, `api/rpc/methods.go:56`)**: `MethodQueued` and intrinsic `MethodForm` reads wake dormant agents inline with per-id singleflight only and no global concurrency semaphore across ~500 reconnecting clients.
4. **Full `attachFormDeltas` replay in `NewAgent` (`internal/figaro/agent.go:263`, `internal/store/topology_form.go:49`)**: every restore scans `stumpFormLog.RangePatches` from LT 1 whenever `prev` falls before the trimmed in-memory patch window.

**Proposed split for Gluck's go-ahead**:
- **Slice 1 (low-risk, immediate)**: (a) read `OutfitName`/`OutfitVersion` from `_meta` in `labelOf` before falling back to `FormState`; (b) persist a `_meta/libretto_refs_migrated` marker once `RefsNeedMigration` is clean; (c) answer `MethodQueued` from `Spool` without waking dormant agents and cap concurrent `restoreOne` calls (e.g. `semaphore = 4`).
- **Slice 2 (structural)**: defer `materializeTurns`/`attachFormDeltas` until transcript read/attach, or add periodic snapshot checkpoints to `stumpFormLog`.

#### `#114`: Fork `--replay` mode (effort-label replay evals)

All four leak surfaces map cleanly (`ForkReminderTexts` in `internal/provider/fork_render.go:13`, client `datetime` injection in `internal/cli/config.go:146`, child `aria_id` birth patch in `internal/angelus/birth_patch.go:188`, and `specDispatcher.dispatch` in `internal/figaro/turn.go:1179`). Three calls needed before building:
1. **Coordinate `<id>:<turn>`**: should `figaro fork <id>:<turn> --replay` cut at `<turn - 1>` and automatically re-submit turn `<turn>`'s recorded inquiry (preserving original `Sender`, `Images`, and `datetime`) with no prompt argument? *(Recommended: yes.)*
2. **Identity masking boundary**: keep the branch's real storage `aria_id` in XWAL/`Registry` (so `ls`/`kill`/`Figaro-Id` spend attribution stay honest) and mask `aria_id`/handle/`system.forked_from` only in the provider-bound snapshot? *(Recommended: yes, projection-only.)*
3. **Unmatched tool calls**: when a different effort level invokes a read tool (`read`/`rg`) with slightly different offsets/args than the recorded turn, fail/stub strictly so zero post-turn host state leaks, or allow read-only `t0-read` sandbox fallback while stubbing outward `figaro`/write calls?
