# Figaro architecture

The durable shape of the system. Conventions drift — when in doubt, read the
source named here and trust it over this file.

## Three roles, one binary

- **CLI** (`internal/cli`) — what the user runs. Connects to the angelus or a
  per-aria socket and renders the stream.
- **Angelus** (`internal/angelus`) — the long-running supervisor (single
  instance via flock). Owns the registry of arias, the **endpoint** each aria is
  reachable at, the daemon-wide session registry, pid bindings, and
  `figaro.list`/`create`/`kill`/`attach`. Survives shells. `figaro rest` stops
  it; the next command respawns it.
- **Agent** (`internal/figaro`) — one per aria (= one conversation), and
  **transient**. Owns the figLog (IR), the chalkboard, the tool registry, and
  the turn loop. Mutations funnel through its **inbox** (an event queue), so
  there is exactly one writer to the chalkboard and the log — e.g. a `figaro
  set` arriving mid-turn is serialized, not raced. Chalkboard *reads* need no
  inbox: snapshots are immutable and published atomically (see the chalkboard
  section).

An agent is a memory decision, not an identity. It is built on demand and
reclaimed when idle, while the aria's endpoint, bindings and background jobs
outlive it — so "the aria" and "the agent serving it" are different lifetimes.
See [reclamation.md](reclamation.md).

## The IR — `internal/message`

The conversation's source of truth: an append-only log of `message.Message`
(stored via figwal, NDJSON segments). Provider wire formats are *derived* from
it and cached; the IR is canonical.

A `Message` has a `Role` (`user` | `assistant` | `tool_result` | `system` |
`system.interrupt`) and `[]Content`. Content `Type` is one of `text`,
`thinking`, `tool_invoke` (assistant calls), `tool_result`, `interrupt`,
`image`. Messages also carry `Patches` (chalkboard mutations riding on a tic),
optional `Usage`, `Model`/`Provider`, `StopReason`, and a monotonic
`LogicalTime` (LT). The IR is provider-agnostic: it holds **no** provider
secrets — notably no Anthropic thinking *signature* (that lives only in the
provider's wire cache; see Provider layer).

### Tool imagery

An `image` block on a tool_result tic carries `ToolCallID`/`ToolName`, naming
the call that produced it (`message.ToolImageContent`). The IR keeps the
association and each encoder decides placement: the Anthropic encoders nest the
image inside the matching `tool_result` block, while the Responses encoder
trails it in the following user message with a caption, because a
`function_call_output` there is a plain string. `message.ToolImagesByCall` is
the single index both Anthropic encoders share, and it deliberately refuses to
claim an image whose call has no `tool_result` in the same message — a claimed
image renders inside a block that never renders, i.e. vanishes.

**Size is a durability constraint, not a taste one.** The tic is ONE figwal
record, and a record that does not fit inside a WAL segment fails the append
and takes the turn with it. So:

- `config.InlineImageBudget()` is the single policy point — two thirds of
  `store.segment_size`, capped at the provider ceiling (~3.5MB base64, past
  which the APIs refuse it anyway). It moves with the store geometry rather
  than being pinned to the smallest legal configuration.
- `tool.FitImage` (in `internal/tool/image.go`) makes a picture fit rather than
  dropping it: pass through → scale to 1568px (Anthropic's own downscale
  threshold) → PNG → JPEG down a quality ladder → shrink 25% and retry. The
  ladder's ORDER follows the source: lossless-in prefers PNG out, JPEG-in
  prefers JPEG out. Resizing is only taken when it actually saves bytes.
  Pure Go (`golang.org/x/image/draw`), no CGo.
- `read` fits at ingest; `harvestToolImages` in the turn loop enforces the
  shared per-message budget and re-fits anything a parallel round squeezed.
  Dropping is the last resort and is always ANNOUNCED in that tool's own
  result text, with the true reason.
- A rescale emits a coordinate factor (`Multiply coordinates by N`) so a model
  clicking what it sees can map back to the real screen. The turn loop's
  second-pass note says FURTHER, because it composes with the ingest note.

## The chalkboard — `internal/chalkboard`

Per-aria key→JSON state. Two namespaces:

- `system.*` — harness-reserved. Providers read these directly
  (`system.credo`, `system.model`, `system.cwd`, `system.cache_control`,
  `system.thinking_budget`, `system.thinking_effort`, …). **Hidden from the
  agent**: `chalkboard.Render` skips any `system.` key.
- everything else — surfaced to the agent. On the tic where a key changes,
  `Render` projects it as a `<system-reminder name="<key>">…</system-reminder>`
  text block (templated if a template exists, else the bare value). This is
  how the agent learns its `aria_id`, `mantra`, skills, etc. A boot patch
  stamps runtime fill-ins each first turn (`system.cwd`, `system.root`, and a
  non-system `aria_id` so the agent can address itself on the CLI).

### Representation — immutable tree, atomic publication

`Snapshot` is a **two-word handle on an immutable AVL tree** with structural
sharing (`tree.go`, lifted from `github.com/jack-work/pstate`), not a map:

```go
type Snapshot struct {
	root    *node   // immutable, shared between snapshots
	version uint64
}
```

- `Clone()` is the **identity function**. It used to deep-copy every value on
  every read (per RPC, per turn, inside `chalkboardString`).
- `Apply` **path-copies**: only the O(k·log n) nodes on the touched paths are
  new; every other subtree is shared with the receiver. A patch that changes
  nothing returns the receiver *pointer-identically*, and `State.Apply` then
  does not even mark the board dirty.
- `Diff` (`treediff.go`) is a merge-join by key with a **pointer-identity
  short-circuit**: `prev == next` proves a whole subtree is unchanged, so a
  one-key delta costs ~10 node comparisons on a 1024-key board. The
  short-circuit is a pure optimisation — the algorithm is correct without it,
  which matters because AVL rotations move nodes.
- Read through `Get`/`Has`/`Len`/`All` (lexical key order); construct from a
  map through `FromMap` (**the seam** — the only constructor); `AsPatch()` is
  the Set-only patch of every entry.
- `Value` (`value.go`) holds the caller's **exact bytes** (`raw`) plus a
  canonical form (compacted, object keys sorted recursively) used **only** for
  `Equal`, computed lazily on first comparison and memoised. Nothing ever
  rewrites stored bytes. That is what keeps `chalkboard.json` byte-identical
  while making equality semantic: a value that changes only in key order,
  whitespace or escape spelling compares equal and fires **no**
  `<system-reminder>`. Numbers compare by literal token, so `1` and `1.0` are
  different edits.
- **A semantically-equal `Set` is a no-op that keeps the OLD bytes** and returns
  the same root pointer. So never ask "did this key change?" with
  `bytes.Equal` against the stored value — it will answer yes forever. Ask the
  board: `cur.Apply(candidate).Diff(cur)`.
- `MarshalJSON`/`UnmarshalJSON` emit and read the **flat object** form
  (`{"key": value, …}`, keys lexical). Three things consume it: the reducible
  chalkboard channel's watermark/state records in the aria store (see
  [arias.md](arias.md) — those are content-hashed), the `figaro.chalkboard` RPC
  response, and `State.Save`'s `chalkboard.json` when a State is opened with a
  path (the agent opens with `""`, i.e. in-memory only). `MarshalJSON`
  delegates to `encoding/json` over a `map[string]json.RawMessage` and never
  hand-rolls the object: `encoding/json`
  compacts a raw message and rewrites `<`, `>`, `&` as `\u003c`, `\u003e`,
  `\u0026`, and that post-processing is part of the bytes already on disk.
- **The custom codec is charged twice.** `encoding/json` re-scans a marshaler's
  output and pre-scans an unmarshaler's input: ~2x on a 15KB board, for
  identical bytes. Hot paths (`chalkboardReduce`, `State.Open`/`Save`) call
  `MarshalJSON`/`UnmarshalJSON` **directly** to skip it;
  `TestSnapshotDirectCodecMatchesEncodingJSON` pins that the two spellings
  agree. Re-measure with `scripts/chalkbench-go.sh` if the question returns.

`State` (`state.go`) is **one writer, many readers**. The writer is the agent's
drain loop (`act` → `applyControlPatch` → `State.Apply`); readers are the
`figaro.chalkboard` handler, `Agent.ApplyOutfit` and
`Agent.chalkboardString`/`chalkboardInt`, all on RPC goroutines. `State`
publishes `{snapshot, dirty}` as one immutable value through an
`atomic.Pointer`, so readers are **lock-free** and always see a complete
board. (Before that, `State.snapshot` was a plain field read with no
happens-before edge — a genuine data race, 11 reports per `-race` run.) One
writer means the update path stores unconditionally; no CAS loop. `Save`
clears the dirty flag with a single non-looping CAS.

Outfits (`internal/outfit`) assemble the boot chalkboard from `config.toml`'s
`default_outfit` chain. `fileName`/`dirName` tables load file bodies as
content envelopes (`{frontmatter|content, filePath}`) — skills come in this
way (`skills.<base>`), so the agent sees a skill's frontmatter and reads its
body on demand. Bundled first-party skills merge under the user's by name.

## The wire protocol — `internal/rpc`

Per-aria request methods: `figaro.qua` (prompt), `figaro.context`,
`figaro.interrupt`, `figaro.set`, `figaro.outfit`, `figaro.chalkboard`,
`figaro.queued`, and `figaro.read` (catch-up/paging). Angelus includes
`figaro.create`/`fork`/`promote`/`kill`/`list`/`attach`,
`pid.bind`/`resolve`/`unbind`, `aria.read`, `aria.page`/`context`/`chalkboard`
(the same reads addressed by aria id), and status/binding persistence.

The transport is NDJSON-framed JSON-RPC 2.0. Every accepted per-aria connection
is automatically subscribed; call `figaro.read` on that connection for initial
state, then keep reading notifications. There is no explicit subscribe method —
though subscription is tracked per-(conn, aria) rather than per-conn, so adding
one is a change of listener count rather than of architecture.

The per-aria socket is served by the **angelus**, not by the agent, and answers
whether or not an agent is resident: `rpc.MethodNeedsAgent` decides which
methods are served from the store and which wake the aria. See
[reclamation.md](reclamation.md).

### Caller identity — `x-internal-figaro-id`

Every request carries the **calling aria's** id in a reserved params field
(`rpc.CallerKey`), injected by both client hops (`WithCaller`) and read back by
`CallerOf`. It rides in `params` and not in a top-level `meta` because the
envelope belongs to **jkrpc**, an external module: `Client.Call` takes
`(method, params, result)` and `HandlerFunc` receives only `(ctx, params)`, so
an envelope slot would cost a jkrpc API change, a release, and a signature
change to every handler — to carry one string `params` already carries.

Injection is generic rather than a field on each request struct because
`figaro.context` and `figaro.chalkboard` send **nil** params; there would be
nothing to embed in, and those methods must still be authenticatable.

This is **not** the target-selection rule. Selection is
`--id > FIGARO_ARIA > pid binding` and answers *which aria am I talking about*;
caller identity answers *which aria am I*, and only `FIGARO_ARIA` can — `--id`
is an argument the caller chose, and a pid binding says which aria a shell is
*attending*, not that it is one.

### Attribution — who sent each part of a message

A user message is **not always one submission**. Consecutive prompts drain and
fold into one message, and they may come from different callers. `Content` and
`livedoc.Node` therefore carry a **`Sender`**, and `aria.Turn` carries
**`InquirySegments`** — the opening question split by who asked it.

`Turn.Inquiry` is unchanged and remains the canonical *text* (search, selection
hashing, the mantra seed and height estimation all want one string); the
segments are the attribution layer over it.

Rendering is decided once, in **`rpc.Attribution`**, so the model, the pager,
the inline view and `figaro show` cannot drift:

| caller | renders as |
|---|---|
| authenticated aria | `aria 76062b18` |
| **the duke** (end user) | the target aria's `duke-title`, else `user` |
| explicit label (`FIGARO_CALLER`) | bare, e.g. `ci-bot` |
| unknown | **nothing at all** — not `unknown`, not a blank row |

**The duke is the end user** — the person the agent serves, as distinct from an
aria or an anonymous script. Their name does not live in shell config: an
*interactive* CLI sends a **placeholder** in `x-caller`, and the agent resolves
it against the **target aria's** chalkboard key **`duke-title`** (default
`user`). Set it in an outfit:

```toml
duke-title = "gluck"
```

`x-caller` is a **typed ref**, not a string, and that is a security property
rather than tidiness: `FIGARO_CALLER` only ever populates `label`, so no value
of it can produce `duke: true`. The guarantee is a type, not a reserved word.

Only an **interactive** process presents the duke — the same TTY signal that
decides the binding policy — so an aria's shell-out cannot speak as its master
by accident. A figaro that deliberately allocates itself a terminal still can:
a known gap, accepted until real authentication, and exactly why none of this
reaches an authorization decision.

`aria ` is a **reserved prefix**: `SanitizeLabel` strips it, so
`FIGARO_CALLER="aria 999"` renders `999` and cannot impersonate proof.

Blocks fold into **runs** — one block per run of consecutive same-sender
submissions, joined by a blank line. So the common case (one sender, or none)
is one block, and a message with no senders encodes **byte-identically** to
before, which matters because the provider wire cache is keyed by LT and holds
signed thinking blocks.

On screen: **one `> input` header** for the whole question however many people
wrote it, each segment prefaced by its sender in the dim register block
timestamps use, indented to sit under the prose.

### Authorization — `internal/authz`

An **Authenticator** turns the credential into an `Identity`; a single
**Policy** maps `(identity, method, raw params)` to allow or deny-with-reason.
`authz.Guard` wraps the whole angelus handler map, so the guarded set is the
served set and no handler opts in individually. Denials return JSON-RPC code
**-32020** carrying the reason verbatim.

`[authz]` in `config.toml` selects both, and **both default off** — an absent
section behaves exactly as figaro did before:

```toml
[authz]
caller_identity = true   # believe x-internal-figaro-id (default false)
policy = "default"       # or "allow-all" (default)
```

`x-caller` is **attribution only and never authorizes**. It lands in
`Identity.Label`, a different field from `FigaroID`, and `SelfTargeted()`
requires an authenticated identity — so an assertion naming its own target
cannot impersonate it. Anyone who can set an environment variable can set the
label; if a policy keyed on it, every rule would be one `FIGARO_CALLER=…` away
from bypass. The separation is a field, not a convention.

Attribution is read **regardless of the switch**: a human is never
authenticated and is exactly the caller a confused aria most needs named.
Disabling the provider withholds *authority*, not identity.

The switch is the point: `FIGARO_ARIA` cannot be turned off, so a server has no
state in which it may doubt it — and a credential that cannot be doubted
authenticates nothing. `AriaHeader` is trust-on-assertion, not proof; it is an
interface so `SO_PEERCRED` or a bearer token drops in without any policy or
handler changing.

The first rule, `NoSelfForkDuringTurn`, refuses a fork an aria issues against
**itself while its own turn is running** — that deadlocks, because fork
coordination rides the agent's single-threaded inbox. Its error text carries the
detached-fork workaround. It is a **guardrail, not a cure**; the fix is to move
trunk state into its own reducible xwal channel (noted at
`angelus.handlers.fork`).

The reply is a **server-authoritative live-render stream**:

- `figaro.aria` (`MethodAriaFrame`) — push one **`Page`**: the single wire
  shape, pulled by `figaro.read` and pushed here. A `Page` is
  `{parts []TurnPart, more, metrics?}`; a `TurnPart` embeds a `Turn`
  (`{turn, inquiry, at, lts, sealed, nodes, live}`) plus `from` /
  `clipped_head` / `clipped_tail`.
- `turn.done` `{reason,idle}` — the turn ended; `idle` says whether queued work
  remains. It is control state, not transcript content.

Node **addresses** are positional: inside a part node `i` is `(turn, from+i)`.
Current snapshot nodes may also serialize a legacy string `id` containing
fig-IR provenance or a provider tool-call receipt; that field is not UI
identity. `Live` is the open suffix — `live.from` is the boundary, and any
ordinal below it is committed and can never change again. `NodeDelta` keeps an
explicit positional `uint64 id` because a delta may reference nodes sparsely.

A pure delta push is a `Page` whose single part carries `live` and no snapshot
`nodes`. A `live` frame with no deltas closes the current streaming suffix, but
only `sealed:true` makes the whole turn final. Every part repeats its `inquiry`
so joining mid-stream does not depend on one opening frame. Push and pull use
one type and overlapping application is idempotent.

## Live-render node model — `internal/livedoc` + `internal/cli`

A live turn is an **append-only, ordinal-stable** `[]Node`. A `Node` is
`prose` | `thinking` | `tool` | `steering`; a tool carries
`Name`/`Args`/`Status` (`running|ok|error`)/`Output`. `internal/compose` builds
nodes from canonical IR. `aria.Server` compares the materialized suffix and
emits `NodeDelta{set,unset,patch}` frames; `aria.Client` folds them. The older
`livedoc.Op`/`DiffNodes` helpers are local utilities, not the current RPC
notification vocabulary. Presentation remains client-owned.

`internal/cli/livelog_bridge.go` connects the folded client model to two
renderers in `internal/livelog/render`:

- **Incipit** paints inline. Closed slices freeze to native terminal scrollback
  exactly once; only the open suffix remains repaintable. The inherent limit is
  that the terminal may push an over-tall live region into scrollback before
  Figaro can repaint it.
- **Transcript** is the opt-in alternate-screen pager over retained/fetched
  history. It continues applying live pages while the user scrolls and pages
  older ranges by `(turn,node)` anchors.

Presentation is a pure client concern. Ctrl-O toggles verbose tool input/output;
Ctrl-T enters or leaves the transcript. Thinking is muted by default, tools are
native widgets, and spinners animate locally. The wire carries semantic node
data rather than terminal rows, ANSI, width, theme, or animation ticks.

## Provider layer — `internal/provider/anthropicsdk`

Translates IR ↔ Anthropic wire and caches the per-aria wire bytes
(`store.Log[[]json.RawMessage]`, keyed by figaro LT).

- **Cache the exact accumulated turn, never a lossy re-encode.** `drainStream`
  returns both the figaro IR and the raw `anthropic.Message`; `Send` caches
  `acc.ToParam()` — the SDK's response→request projection, which preserves
  thinking-block **signatures** and `redacted_thinking` verbatim. Re-encoding
  from the IR would drop the signature (the IR has no home for it) and a
  replayed unsigned thinking block is a 400. The cache-miss fallback drops
  thinking blocks rather than emit unsigned ones.
- **Extended thinking** (`assemble.go::applyThinking`). Two model families:
  adaptive (Opus 4.6/4.7/4.8, Sonnet 4.6) take `{type:"adaptive"}` +
  `output_config:{effort}` and ignore a token budget; older models take
  `{type:"enabled", budget_tokens}`. Crucially, set `display:"summarized"` —
  the Claude-Code/OAuth default is `"omitted"` (signature only, empty thinking
  text). Knobs: `system.thinking_effort` (low|medium|high|xhigh|max; default
  high) and `system.thinking_budget`.
- **Automatic prompt caching** (`resolveCacheControl` / `markCacheBreakpoints`)
  — see cache-control.md.
- **Streamed tool input** (`assemble.go::eagerToolStreaming`). Anthropic
  **buffers each tool parameter value** until it is complete before streaming
  it, so a large argument arrives in one lump: measured on a 5 KB `write`,
  seven fragments in the first half-second (the short `path`) and then 25
  seconds of silence before the rest. `system.eager_tool_streaming = true`
  sets the per-tool `eager_input_streaming` field, which turns the buffering
  off; absent or false omits the field and keeps the API default.
  **Copilot refuses it**: the Anthropic-dialect endpoint rejects the field
  with a 400, so `anthropicsdk.Provider.NoEagerToolStreaming` (set by the
  copilot provider) drops it there whatever the board says.
  **The price is real and it is paid in whole tool calls.** The buffering is
  what guarantees a parameter arrives as complete, ESCAPED JSON text. Without
  it the model's own escaping mistakes arrive verbatim: raw tabs, raw newlines,
  bare quotes, a string value that never closes. Measured 2026-08-06 — five
  turns, every one an `edit` carrying Go source, every affected aria carrying
  the key, and the one aria without it never hit it once. **Anthropic documents
  this outcome**: the input is streamed "without server-side buffering or JSON
  validation", so invalid JSON is contractual, and the prescribed response is
  not to repair it but to "report the failure back to Claude instead".
  ONE RULE — IF IT DOES NOT PARSE, IT DOES NOT RUN — in two mechanisms:
  1. `quarantineMalformedToolInput` (decode.go). The bytes are kept verbatim
     under `message.MalformedArgsKey`, which is a legal JSON object — so the
     tool_use replays and its tool_result is not orphaned — and the call is
     REFUSED rather than guessed: half-parsed `edit` arguments would write the
     wrong bytes into a source file, and the damage is not invertible anyway
     (measured across five payloads: 83 escape characters in one, zero in
     another, and in that one a required argument never transmitted at all).
     The agent returns the documented result — `is_error` with content
     `{"INVALID_JSON": "<what arrived>"}`, built by the encoder — and the model
     resends. **One bad block costs one tool call, not the turn**; before this
     it cost the thinking, the prose, and every tool call already streamed.
     An earlier control-character repair (81137e6) was reverted in b64d0b6: it
     was sound but it was a second rule, and it rewrote model output where
     nothing downstream could tell.
  2. `compose.toolNode` unwraps the envelope back into `Node.Input`, so a
     failed call is drawn as THE BYTES THAT ARRIVED rather than as a sentinel
     argument row — which is the whole point of unbuffered streaming, and the
     bytes ride the message, so it reads the same after a reload as it did
     live. **figaro never stands eager streaming down**: an earlier per-aria
     latch was reverted, because the model simply resends and a resample
     almost always succeeds.
  `provider.tool_use.unescaped_chunk` (stream.go) is the canary that named the
  cause and will name the next one: it fires on the first argument fragment
  carrying a raw control character, seconds before the marshal fails, and
  carries `wire.doubled_escape` — false means the fragment was already
  single-escaped when it reached us, true would mean something below us decoded
  twice (`TestWireIsDecodedExactlyOnce` says that something is neither figaro
  nor the SDK).
- **Auth** (`auth.go`) — OAuth via hush; Claude-Code identity headers + beta
  flags. `anthropic-beta` does not need `interleaved-thinking` for adaptive
  models.

### The provider binding is live, not a birthmark

`system.provider` is chalkboard state like any other key, and the board is
authoritative. The agent holds a `ProviderFactory` and re-resolves the binding
at the top of **every** provider round (`internal/figaro/provbind.go`,
`syncProvider`), after that round's queued `set`s are serviced — so
`figaro set system.provider copilot` (or a re-applied outfit that moves the
aria) takes effect on the next round, with no restart and no fork. Before
this, the instance was frozen at create/restore: an aria whose provider was
wedged (a persistent `overloaded_error`, say) could not be moved off it while
the angelus stayed up, and `figaro status` cheerfully reported the *old*
provider while the board said otherwise.

- The binding is `{name, knobs, instance}` published through an
  `atomic.Pointer` — written by the drain loop, read lock-free by
  status/metrics on RPC goroutines.
- **Model is not a rebuild trigger.** Every provider resolves `system.model`
  from the per-turn snapshot inside `Send`; rebuilding on a model change would
  only discard the in-memory wire projection. Build-time knobs
  (`system.reminder_renderer`, `system.use_official_sdk`, `system.max_tokens`)
  do trigger one.
- A factory error **fails the turn** naming the provider. Falling back to the
  old instance would silently contradict the board — the exact confusion the
  bug produced.
- Switching is safe because the IR is canonical and each provider owns its
  own translation channel: the new provider simply re-projects the history it
  has not seen. That makes the cache-miss encoder load-bearing — it must drop
  unsigned thinking blocks (both the SDK and raw Anthropic encoders do).

## Tools: bash & backgrounding

The bash tool (`internal/tool/bash.go`, `exec_local.go`) runs each command via
`bash -c` in its **own process group** (`Setpgid`). It waits up to `yieldMs`
(default 10s); if the command is still running it **auto-backgrounds as a
tracked session** (not killed) — follow up with the **`process`** tool. Args:
`background:true` (background immediately), `timeout` (hard-kill deadline,
default 30m), `pty`, `yieldMs`. `killProcessGroup` SIGKILLs the whole group
only on **timeout or cancel**, never on normal completion.

Completion is signalled by the stdout/stderr **pipe reaching EOF**, not the
foreground exit — and that has consequences for `&`:

- A bare `cmd &` child **inherits** the stdout/stderr pipe, so the command
  keeps "running" until that child finishes and backgrounds as a session. Its
  work is captured, but **not done when the call returns** — poll via the
  `process` tool, or use `cmd & wait`, or just run serially. (An agent that
  fires parallel `git clone … &` and immediately `ls` will see incomplete
  results — it must wait.)
- A **redirected** `cmd >/dev/null 2>&1 &` releases the pipe, so the call
  returns immediately and that child **orphans**: it keeps running to
  completion but is untracked — no captured output, no supervision, invisible
  to the `process` tool. Fine for true fire-and-forget (e.g. a quick
  `figaro set … &`); don't rely on it for work you need to observe.

Rule of thumb: don't background with bare `&` and assume completion. Use
`background:true` + the `process` tool, `& wait`, or serial commands.

Sessions live on the **daemon** (`Angelus.Sessions`), not on the agent, keyed by
aria id as scope — so a backgrounded job survives its agent being reclaimed and
keeps its id across a wake. `figaro kill` reaps an aria's sessions; hibernation
does not.

## Storage

State root `~/.local/state/figaro/arias/`: parallel XWAL trees in `ir/`,
`chalkboard/`, and `translations/<provider>/`, plus `_meta/<id>.json`
for list/status metadata. See arias.md for reading these safely.

`XwalBackend` memoizes one row cache per (aria, channel) so a reader sees the
writer's appends lock-free. Those caches are bounded and evictable; what they
cost and when they are released is [reclamation.md](reclamation.md).
