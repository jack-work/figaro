# Figaro architecture

The durable shape of the system. Conventions drift: when in doubt, read the
source named here and trust it over this file.

## Three roles, one binary

- **CLI** (`internal/cli`): what the user runs. Connects to the daemon's door
  and renders the stream.
- **Angelus** (`internal/angelus`): the long-running supervisor (single
  instance via flock). Owns the registry of arias, the **endpoint** each aria is
  reachable at, the daemon-wide session registry, pid bindings, and
  `figaro.list`/`create`/`kill`/`attach`. Survives shells. `figaro rest` stops
  it; [cli.md](../cli.md) says when the next command respawns it.
- **Agent** (`internal/figaro`): one per aria (= one conversation), and
  **transient**. Owns the figLog (IR), the form, the tool registry, and
  the turn loop. Mutations funnel through its **inbox** (an event queue), so
  there is exactly one writer to the form and the log: e.g. a `figaro
  set` arriving mid-turn is serialized, not raced. Form *reads* need no
  inbox: snapshots are immutable and published atomically (see the form
  section).

An agent is a memory decision, not an identity. It is built on demand and
reclaimed when idle, while the aria's endpoint, bindings and background jobs
outlive it: so "the aria" and "the agent serving it" are different lifetimes.
See [reclamation.md](../contributing/reclamation.md).

## The IR: `internal/message`

The conversation's source of truth: an append-only log of `message.Message`
(stored via figwal, NDJSON segments). Provider wire formats are *derived* from
it and cached; the IR is canonical.

A `Message` has a `Role` (`user` | `assistant` | `tool_result` | `system` |
`system.interrupt`) and `[]Content`. Content `Type` is one of `text`,
`thinking`, `tool_invoke` (assistant calls), `tool_result`, `interrupt`,
`image`. Messages also carry `Patches` (form mutations riding on a tic),
optional `Usage`, `Model`/`Provider`, `StopReason`, and a monotonic
`LogicalTime` (LT). The IR is provider-agnostic: it holds **no** provider
secrets: notably no Anthropic thinking *signature* (that lives only in the
provider's wire cache; see Provider layer).

### Tool imagery

An `image` block on a tool_result tic carries `ToolCallID`/`ToolName`, naming
the call that produced it (`message.ToolImageContent`). The IR keeps the
association and each encoder decides placement: the Anthropic encoders nest the
image inside the matching `tool_result` block, while the Responses encoder
trails it in the following user message with a caption, because a
`function_call_output` there is a plain string. `message.ToolImagesByCall` is
the single index both Anthropic encoders share, and it deliberately refuses to
claim an image whose call has no `tool_result` in the same message, a claimed
image renders inside a block that never renders, i.e. vanishes.

**Size is a durability constraint, not a taste one.** The tic is ONE figwal
record, and a record that does not fit inside a WAL segment fails the append
and takes the turn with it. So:

- `config.InlineImageBudget()` is the single policy point: two thirds of
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

## The form: `internal/form`

Per-aria key→JSON state. Two namespaces:

- `system.*`: harness-reserved. Providers read these directly
  (`system.credo`, `system.model`, `system.cwd`, `system.cache_control`,
  `system.thinking_budget`, `system.thinking_effort`, …). **Hidden from the
  agent**: `form.Render` skips any `system.` key.
- everything else: surfaced to the agent. On the tic where a key changes,
  `Render` projects it as a `<system-reminder name="<key>">…</system-reminder>`
  text block (templated if a template exists, else the bare value). This is
  how the agent learns its `aria_id`, `mantra`, skills, etc. A boot patch
  stamps runtime fill-ins each first turn (`system.cwd`, and a
  non-system `aria_id` so the agent can address itself on the CLI).

### Representation: immutable tree, atomic publication

`Snapshot` is a **two-word handle on an immutable AVL tree** with structural
sharing (`tree.go`, lifted from `github.com/jack-work/pstate`), not a map:

```go
type Snapshot struct {
	root    *node   // immutable, shared between snapshots
	version uint64
}
```

- `Clone()` is the **identity function**. It used to deep-copy every value on
  every read (per RPC, per turn, inside `formString`).
- `Apply` **path-copies**: only the O(k·log n) nodes on the touched paths are
  new; every other subtree is shared with the receiver. A patch that changes
  nothing returns the receiver *pointer-identically*, and `State.Apply` then
  does not even mark the board dirty.
- `Diff` (`treediff.go`) is a merge-join by key with a **pointer-identity
  short-circuit**: `prev == next` proves a whole subtree is unchanged, so a
  one-key delta costs ~10 node comparisons on a 1024-key board. The
  short-circuit is a pure optimisation: the algorithm is correct without it,
  which matters because AVL rotations move nodes.
- Read through `Get`/`Has`/`Len`/`All` (lexical key order); construct from a
  map through `FromMap` (**the seam**: the only constructor); `AsPatch()` is
  the Set-only patch of every entry.
- `Value` (`value.go`) holds the caller's **exact bytes** (`raw`) plus a
  canonical form (compacted, object keys sorted recursively) used **only** for
  `Equal`, computed lazily on first comparison and memoised. Nothing ever
  rewrites stored bytes. That is what keeps the form channel byte-identical
  while making equality semantic: a value that changes only in key order,
  whitespace or escape spelling compares equal and fires **no**
  `<system-reminder>`. Numbers compare by literal token, so `1` and `1.0` are
  different edits.
- **A semantically-equal `Set` is a no-op that keeps the OLD bytes** and returns
  the same root pointer. So never ask "did this key change?" with
  `bytes.Equal` against the stored value: it will answer yes forever. Ask the
  board: `cur.Apply(candidate).Diff(cur)`.
- `MarshalJSON`/`UnmarshalJSON` emit and read the **flat object** form
  (`{"key": value, …}`, keys lexical). Three things consume it: the reducible
  form channel's watermark/state records in the aria store (see
  [arias.md](arias.md): those are content-hashed), the `figaro.form` RPC
  response, and `State.Save`'s the form channel when a State is opened with a
  path (the agent opens with `""`, i.e. in-memory only). `MarshalJSON`
  delegates to `encoding/json` over a `map[string]json.RawMessage` and never
  hand-rolls the object: `encoding/json`
  compacts a raw message and rewrites `<`, `>`, `&` as `\u003c`, `\u003e`,
  `\u0026`, and that post-processing is part of the bytes already on disk.
- **The custom codec is charged twice.** `encoding/json` re-scans a marshaler's
  output and pre-scans an unmarshaler's input: ~2x on a 15KB board, for
  identical bytes. Hot paths (`formReduce`, `State.Open`/`Save`) call
  `MarshalJSON`/`UnmarshalJSON` **directly** to skip it;
  `TestSnapshotDirectCodecMatchesEncodingJSON` pins that the two spellings
  agree. Re-measure with `scripts/formbench-go.sh` if the question returns.

`State` (`state.go`) is **one writer, many readers**. The writer is the agent's
drain loop (`act` → `applyControlPatch` → `State.Apply`); readers are the
`figaro.form` handler and
`Agent.formString`/`formInt`, all on RPC goroutines. `State`
publishes `{snapshot, dirty}` as one immutable value through an
`atomic.Pointer`, so readers are **lock-free** and always see a complete
board. (Before that, `State.snapshot` was a plain field read with no
happens-before edge, a genuine data race, 11 reports per `-race` run.) One
writer means the update path stores unconditionally; no CAS loop. `Save`
clears the dirty flag with a single non-looping CAS.

Outfits (`internal/outfit`) assemble a form patch from outfit NAMES and the
layers they declare, defaulting to `config.toml`'s `default_outfit`
([outfits.md](outfits.md)). Names and data are separate axes: `-O` takes names,
`-S`/`-D` take keys and removals, and the daemon resolves the names once, at
its API boundary, so nothing below reads a file. `fileName`/`dirName` tables load file bodies as
content envelopes (`{frontmatter|content, filePath}`): skills come in this
way (`skills.<base>`), so the agent sees a skill's frontmatter and reads its
body on demand. Bundled first-party skills merge under the user's by name.

## The wire protocol: `internal/rpc`

Per-aria request methods: `figaro.qua` (prompt), `figaro.context`,
`figaro.interrupt`, `figaro.set`, `figaro.form`,
`figaro.queued`, and `figaro.read` (catch-up/paging). Angelus includes
`figaro.create`/`fork`/`promote`/`kill`/`list`/`attach`,
`pid.bind`/`resolve`/`unbind`, `aria.read`, `aria.page`/`context`/`form`
(the same reads addressed by aria id), and status/binding persistence.

`aria.read` answers one page, capped at 1000 entries whatever the limit asks,
and names the next LT in `next_from`. Anything that wants the whole aria
(`export`, the CLI-side turn resolution behind `attend <id>:N` and `status`)
goes through
`sdk.Angelus.IRAll`, which walks `next_from` to the end; a single call there
is issue #23 again.

The transport is NDJSON-framed JSON-RPC 2.0 over one door per daemon. An aria
method names its aria in `params.figaro_id`. A connection follows an aria
after `figaro.follow` or a prompt there; reads never subscribe. Follow, call
`figaro.read` for initial state, then keep reading notifications, each of which
names its aria.

Aria methods are served by the **angelus**, not by the agent, and answer
whether or not an agent is resident: `rpc.MethodNeedsAgent` decides which
methods are served from the store and which wake the aria. See
[reclamation.md](../contributing/reclamation.md).

### Caller identity: `x-internal-figaro-id`

Every request carries the **calling aria's** id in a reserved params field
(`rpc.CallerKey`), injected by both client hops (`WithCaller`) and read back by
`CallerOf`. It rides in `params` and not in a top-level `meta` because the
envelope belongs to **jkrpc**, an external module: `Client.Call` takes
`(method, params, result)` and `HandlerFunc` receives only `(ctx, params)`, so
an envelope slot would cost a jkrpc API change, a release, and a signature
change to every handler: to carry one string `params` already carries.

Injection is generic rather than a field on each request struct because
`figaro.context` and `figaro.form` send **nil** params; there would be
nothing to embed in, and those methods must still be attributed.

This is **not** the target-selection rule. Selection is
`--id > FIGARO_ARIA > pid binding` and answers *which aria am I talking about*;
caller identity answers *which aria am I*, and only `FIGARO_ARIA` can: `--id`
is an argument the caller chose, and a pid binding says which aria a shell is
*attending*, not that it is one.

### Attribution: who sent each part of a message

A user message is **not always one submission**. Consecutive prompts drain and
fold into one message, and they may come from different callers. `Content` and
`livedoc.Node` therefore carry a **`Sender`**, and `aria.Turn` carries
**`InquirySegments`**: the opening question split by who asked it.

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
| unknown | **nothing at all**: not `unknown`, not a blank row |

**The duke is the end user**: the person the agent serves, as distinct from an
aria or an anonymous script. Their name does not live in shell config: an
*interactive* CLI sends a **placeholder** in `x-caller`, and the agent resolves
it against the **target aria's** form key **`duke-title`** (default
`user`). Set it in an outfit:

```toml
duke-title = "gluck"
```

`x-caller` is a **typed ref**, not a string, and that is a security property
rather than tidiness: `FIGARO_CALLER` only ever populates `label`, so no value
of it can produce `duke: true`. The guarantee is a type, not a reserved word.

Only an **interactive** process presents the duke: the same TTY signal that
decides the binding policy: so an aria's shell-out cannot speak as its master
by accident. A figaro that deliberately allocates itself a terminal still can,
which is why authorization reads the door's principal and never the duke.

`aria ` is a **reserved prefix**: `SanitizeLabel` strips it, so
`FIGARO_CALLER="aria 999"` renders `999` and cannot impersonate proof.

Blocks fold into **runs**: one block per run of consecutive submissions
with the same sender and principal, joined by a blank line. So the common case (one sender, or none)
is one block, and a message with no senders encodes **byte-identically** to
before, which matters because the provider wire cache is keyed by LT and holds
signed thinking blocks.

On screen: **one `> input` header** for the whole question however many people
wrote it, each segment prefaced by its sender in the dim register block
timestamps use, indented to sit under the prose.

### Authorization: `internal/authz`

**Admission is per listener.** A connection carries the principal its
listener's authenticator stamped, and nothing in params can change it:

| listener | authenticator | principal |
|---|---|---|
| CLI door (`angelus.sock`) | `Peercred.Admit`: `SO_PEERCRED` on the unix socket | `user:<uid>`; another uid only through `[authz.peercred] uids`. Off linux, any unix peer is the daemon's uid. A non-unix peer is refused. |
| web door (`[door.web]`) | per upgrade request, `authn = "upstream"` or `"bearer"`; any other value and the door does not start | upstream: the edge's `Remote-User` / `Remote-Groups`, scoped by `[authz.upstream] issuer`, trusted only on that socket and never on the CLI door's path. bearer: a session bound at the upgrade, refused once the token expires; the client reconnects. A bearer starting `fgk_` is an api key (Troupe api keys, below) and goes only to the key table; anything else goes only to the issuers. |
| node door (`[door.tls]`) | `peer.Door.Admit`: mTLS against `[peers.<node>]` | `node:<id>`; a forwarded caller is its bearer, verified again against `[[authz.issuers]]` or, for an api key, this node's key table, and refused when neither applies |

**Issuers.** `[[authz.issuers]]` lists the identity issuers this node
trusts (`issuer`, `jwks`, `userinfo`, `audience`, `types`, `users`,
`clients`). A token goes to the issuer its `iss` names and is verified
there. Every principal and group an issuer or the edge produces carries
the issuer's scope, its URL without the scheme:
`user:jack@auth.kelliher.info`, `sandbox:figbox@auth.kelliher.info`, the
group `figaro-admin@auth.kelliher.info`. The scope is derived, so an
issuer cannot take another's names, and two issuers with one scope are
refused. Policies name the scoped spelling; one that grants an unscoped
user (other than a uid) or group still loads, and each such name is
logged with what to write. The retired single `[authz.bearer]` table is
refused by name.

**Every call is decided.** `authz.Guard` wraps the whole handler map with
`authz.Enforce`: each method maps to one verb (`api/rpc/verbs.go`; an
unclassified method fails `TestEveryMethodHasAVerb`), checked on
`aria:<id>` when the request names an aria and on the daemon otherwise.
`figaro.hello` is exempt; `figaro.list` is served and drops the rows the
caller cannot read before it pages, and `node.resolve` answers only the refs
the caller may read. A call that reaches a second resource
checks it too: `study` reads its form, `cast` sets its role, `lineage`
reads `against`, `kill -r` needs `kill` on every aria it takes. A prompt or
`figaro.follow` subscribes the connection only where the caller holds
`listen`. Denials are JSON-RPC **-32020** with the reason verbatim. A request
for an aria another node holds is forwarded before this node's policy runs,
and the owner decides it (the node door, under "JSON-RPC surface" in
`ARCHITECTURE.md`).

Ids are resolved before the policy sees them, and a prefix resolves only
among arias the caller may read, so resolution cannot list or expand an id
the caller could not read.

**The policy** is TOML at `[authz] policy` (relative to the config dir):
roles of verbs on selectors (`aria:<id>`, `role:@<name>`, `owner:self`,
`lineage:<id>`, `sandbox:<name>`, `troupe:<name>`, `*`), bound to principals. With none
configured, the daemon's own principal holds every verb and nobody else holds
any. `outfit reload` and `angelus.configure` reload it; a file that fails to
load leaves the previous policy in force. `owner:self` reads `minted_by`,
which the birth record writes once and nothing rewrites.

**Sandboxes.** `use` on `sandbox:<name>` is checked on the dressed form at
create, fork, bind, import and `figaro.set`. An aria minted by a sandbox
principal is pinned to that sandbox. Under `local` or `bwrap` a `system.cwd`
the caller names is a host path and needs `use` on `sandbox:local`; the one
the daemon assigns is under "Workspaces under a sandbox" below.

**Troupes.** `join` on `troupe:<name>` is checked by `troupe.sign` and
`troupe.identity` themselves, and `admin` on it by `troupe.entries` (from a
caller), `troupe.leave`, `troupe.policy.set` and `troupe.apikey.mint`, since
the guard knows only arias and the daemon. `troupe.apikey.revoke` and
`troupe.apikey.ls` decide for themselves too (below).

**The troupe layer.** A call the node's own policy refuses may still be
allowed by the troupe policy in force (below, under Troupe membership), but
only where the node's policy grants the principal `troupe:<name>` the same
verb on the same resource. That binding is what the node lends the troupe:
`troupe join --delegate all` binds it to an all-granting role, `none` binds
nothing. A denial caused by the missing loan says so. The troupe never
decides `use` on `sandbox:local` or `admin` on `troupe:<name>`, whatever it
or the loan says. No peercred uid, token client or edge service may resolve
to kind `troupe`. The troupe policy's `[[issuers]]` are added to the node's
own unless `[authz] troupe_issuers = false`: https only, never one of the
node's own URLs or scopes, and never trusted for the edge's reverify.

`x-caller` is **attribution only and never authorizes**. It lands in
`Identity.Label`, a different field from `FigaroID`, so an assertion naming
its own target cannot impersonate it. The stamped principal rides beside it
on every prompt block (`message.Content.Principal`), so a transcript says who
spoke as well as who claimed to.

An aria may fork itself from inside its own running turn. The fork handler
calls `Backend.ForkWith` directly rather than queueing on the agent's inbox,
so nothing waits on the turn that issued it
(`TestFigaroCanForkItselfFromInsideItsOwnTurn`).

The reply is a **server-authoritative live-render stream**:

- `figaro.aria` (`MethodAriaFrame`): push one **`Page`**: the single wire
  shape, pulled by `figaro.read` and pushed here. A `Page` is
  `{parts []TurnPart, more, metrics?}`; a `TurnPart` embeds a `Turn`
  (`{turn, inquiry, at, lts, sealed, nodes, live}`) plus `from` /
  `clipped_head` / `clipped_tail`.
- `turn.done` `{reason,idle}`: the turn ended; `idle` says whether queued work
  remains. It is control state, not transcript content.

Node **addresses** are positional: inside a part node `i` is `(turn, from+i)`.
Current snapshot nodes may also serialize a legacy string `id` containing
fig-IR provenance or a provider tool-call receipt; that field is not UI
identity. `Live` is the open suffix: `live.from` is the boundary, and any
ordinal below it is committed and can never change again. `NodeDelta` keeps an
explicit positional `uint64 id` because a delta may reference nodes sparsely.

A pure delta push is a `Page` whose single part carries `live` and no snapshot
`nodes`. A `live` frame with no deltas closes the current streaming suffix, but
only `sealed:true` makes the whole turn final. Every part repeats its `inquiry`
so joining mid-stream does not depend on one opening frame. Push and pull use
one type and overlapping application is idempotent.

## Live-render node model: `internal/livedoc` + `internal/cli`

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

Presentation is a pure client concern. M-m toggles verbose tool output and the node addresses;
Ctrl-T enters or leaves the transcript. Thinking is muted by default, tools are
native widgets, and spinners animate locally. The wire carries semantic node
data rather than terminal rows, ANSI, width, theme, or animation ticks.

## Provider layer: `internal/provider/anthropic`

Direct HTTP+SSE, no SDK. Translates IR <-> Anthropic wire and caches the
per-aria wire bytes (`store.Log[[]json.RawMessage]`, keyed by figaro LT).

- **Cache the exact accumulated turn, never a lossy re-encode.** `Send`
  caches the sanitized provider-native assistant response, which preserves
  thinking-block **signatures** and `redacted_thinking` verbatim. Re-encoding
  from the IR would drop the signature (the IR has no home for it) and a
  replayed unsigned thinking block is a 400. The cache-miss fallback drops
  thinking blocks rather than emit unsigned ones.
- **A stream that closed cleanly is not yet a reply.** The drained message
  goes through `provider.CheckCompletion` before it lands. Two faults: no content blocks at
  all, whatever the stop reason; or a stop reason that is not a way of
  finishing. `provider.AnthropicStopReason` is the one table for the wire's
  words, and a word it does not know (`refusal`, `content_filter`, anything
  newer) is `StopError`, never `""`. On a fault the content the model did
  produce lands marked `error` and the send returns the fault, so the turn
  ends `error: <provider>: the provider refused the completion
  (stop_reason=refusal): ...` with the wire's word in it. Before this an
  upstream filter answering 200 with an empty body recorded a normal `stop`
  with no content, the aria went quiet, and since the trigger stayed in
  history every later turn did the same (jack-work/figaro#24). The SSE
  drain no longer synthesises `end_turn` for a `message_stop` that arrived
  without one either; the empty string travels and decodes as the fault it is.
- **Extended thinking** (`anthropic.go::applyThinking`). Two model families:
  adaptive (Opus 4.6/4.7/4.8, Sonnet 4.6) take `{type:"adaptive"}` +
  `output_config:{effort}` and ignore a token budget; older models take
  `{type:"enabled", budget_tokens}`. Crucially, set `display:"summarized"` -
  the Claude-Code/OAuth default is `"omitted"` (signature only, empty thinking
  text). Knobs: `system.thinking_effort` (low|medium|high|xhigh|max; default
  high) and `system.thinking_budget`.
- **Automatic prompt caching** (`markCacheBreakpoints`)
: see cache-control.md.
- **Auth**: OAuth via hush; Claude-Code identity headers + beta
  flags. `anthropic-beta` does not need `interleaved-thinking` for adaptive
  models.

### Responses dialect: `internal/provider/responses`

Which provider and transport a model reaches is decided in
`internal/provider/routed`; see [providers.md](providers.md). The Responses
dialect sends the locally reconstructed conversation as one request per turn,
over a WebSocket or an HTTP SSE stream as the route chose. There is no remote
continuation dependency, `previous_response_id`, or durable server-side
session.

- `stream.go` forwards argument deltas immediately and correlates
  calls by call ID, announced item ID, then an **explicit** output index.
  Copilot may re-encrypt item IDs between events; an absent index must not
  accidentally match tool zero. Complete calls can execute while other output
  is still streaming, but partial JSON never executes.
- `response.incomplete` terminates the read without waiting for socket closure.
  `max_output_tokens` means length-limited output; filtering, unsolicited
  steering, failure and cancellation stop with an error instead of pretending
  to be a normal completion. Received text and completed calls survive failed
  reads. Completed native items retain opaque reasoning for local replay.
- Malformed completed arguments use `message.MalformedArgs`, the same refusal
  mechanism as other providers: no execution, an `INVALID_JSON` tool result,
  and the original bytes retained. The native cache also carries the quarantine
  envelope so the next request remains valid. A call already dispatched cannot
  change arguments later, and an authentication rejection is retried only
  before any assistant output has been observed. The WebSocket client exposes
  handshake status: a 401 refreshes once; a 403, 429 or other rejection does
  not invalidate credentials. Redirects are refused, and the per-message read
  budget is 32 MiB rather than the client library's 32 KiB default.
- `capabilities.go` validates published model-specific request constraints
  before credential resolution. Astra rejects sampling controls and supports
  `low`, `medium`, `high`, `xhigh`, `max` reasoning effort. Unknown model families
  remain unconstrained; settings are never silently discarded.
- `wirelog.BeginStream` makes both transports visible in `doctor provider`
  (method `WS` or `SSE`),
  including while in flight. It records timing, byte/event counts, argument
  deltas, unmatched deltas and terminal status, not headers, argument text,
  opaque identifiers or free-form provider errors. An unknown event increments
  a counter rather than growing a log of provider-controlled strings.

### The provider binding is live, not a birthmark

`system.provider` is form state like any other key, and the board is
authoritative. The agent holds a `ProviderFactory` and re-resolves the binding
at the top of **every** provider round (`internal/figaro/provbind.go`,
`syncProvider`), after that round's queued `set`s are serviced: so
`figaro set system.provider copilot` (or a re-applied outfit that moves the
aria) takes effect on the next round, with no restart and no fork. Before
this, the instance was frozen at create/restore: an aria whose provider was
wedged (a persistent `overloaded_error`, say) could not be moved off it while
the angelus stayed up, and `figaro status` cheerfully reported the *old*
provider while the board said otherwise.

- The binding is `{name, knobs, instance}` published through an
  `atomic.Pointer`: written by the drain loop, read lock-free by
  status/metrics on RPC goroutines.
- **Model is not a rebuild trigger.** Every provider resolves `system.model`
  from the per-turn snapshot inside `Send`; rebuilding on a model change would
  only discard the in-memory wire projection. Build-time knobs
  (`system.reminder_renderer`, `system.max_tokens`)
  do trigger one.
- A factory error **fails the turn** naming the provider. Falling back to the
  old instance would silently contradict the board: the exact confusion the
  bug produced.
- Switching is safe because the IR is canonical and each provider owns its
  own translation channel: the new provider simply re-projects the history it
  has not seen. That makes the cache-miss encoder load-bearing: it must drop
  unsigned thinking blocks (both the SDK and raw Anthropic encoders do).

### Credentials belong to hush, not to an aria: `internal/auth`

A provider instance is per-aria, so anything cached on one is cached N
times. That is fine for a wire projection and wrong for a credential.

figaro holds no credential state at all. hush does, one agent per machine,
one copy of each secret, and it does both jobs a credential needs:

- **Refresh**, for an OAuth pair (anthropic): the agent renews before expiry
  and every reader sees the current access token.
- **Exchange**, for a credential an API will not take directly (copilot):
  the GitHub token is traded for a ~30-minute session token, which arrives
  with the API host to spend it at. hush's `copilot` grant performs it.

Both run on the same machinery, single-flight, persist-before-publish,
proactive renewal at 0.7 of the token's lifetime, backoff, encryption at
rest, and a permanent-vs-transient error taxonomy. figaro asks over a unix
socket on each resolve; **that round-trip is the sharing**. A cache in
figaro would reintroduce exactly what this replaced: the Copilot session
token used to be cached on the provider instance, so a fleet paid one
exchange per conversation, and the herd of retries earned a 403 that figaro
rendered as "No provider connected".

- `provider.Registration.ExchangeGrant` / `ExchangeURL` declare that a
  provider's bearer is minted rather than stored. `ExchangeURL` returning
  `""` means this installation does not exchange (Copilot's direct mode
  presents the GitHub token unchanged).
- For such a provider the credential chain is **only** the hush strategy
  (`internal/cli/provider.go`). The env var and the stored `api_key` are the
  DURABLE secret, not a bearer; presenting either would present the wrong
  credential. They are handed to hush once by
  `ensureExchangeCredential`, which is also the migration path off
  `providers/copilot.toml`, and never read again.
- Routing travels with the token: hush returns `api_base` metadata,
  `auth.OAuth` publishes it, and `auth.Endpoint` walks an `Aggregate` to
  find it.
- **The agent outlives the binary that spawned it.** figaro's hush is
  embedded, figaro re-execs itself as the agent, so a new figaro can be
  talking to an agent from the previous release, which answers a grant
  registration with "client_id is required". `ensureAgentKnowsGrant` asks
  the agent what grants it supports (`Client.SupportsGrant`) and, when
  figaro owns it, restarts its own child. An external agent is the user's,
  so figaro says what is wrong instead of killing it.
- **A failed mint is not a missing credential.** `authFailureHint`
  (`internal/cli/provider.go`) keeps them apart: no credential at all gets
  the connect-a-provider menu; a failed exchange keeps the provider's own
  status code and says to retry.

## Tools: bash & backgrounding

The bash tool (`internal/tool/bash.go`, `exec_local.go`) runs each command via
`bash -c` in its **own process group** (`Setpgid`). It waits up to `yieldMs`
(default 10s); if the command is still running it **auto-backgrounds as a
tracked session** (not killed): follow up with the **`process`** tool. Args:
`background:true` (background immediately), `timeout` (hard-kill deadline,
default 30m), `pty`, `yieldMs`. `killProcessGroup` SIGKILLs the whole group
only on **timeout or cancel**, never on normal completion.

Completion is signalled by the stdout/stderr **pipe reaching EOF**, not the
foreground exit, and that has consequences for `&`:

- A bare `cmd &` child **inherits** the stdout/stderr pipe, so the command
  keeps "running" until that child finishes and backgrounds as a session. Its
  work is captured, but **not done when the call returns**: poll via the
  `process` tool, or use `cmd & wait`, or just run serially. (An agent that
  fires parallel `git clone … &` and immediately `ls` will see incomplete
  results: it must wait.)
- A **redirected** `cmd >/dev/null 2>&1 &` releases the pipe, so the call
  returns immediately and that child **orphans**: it keeps running to
  completion but is untracked: no captured output, no supervision, invisible
  to the `process` tool. Fine for true fire-and-forget (e.g. a quick
  `figaro set … &`); don't rely on it for work you need to observe.

Rule of thumb: don't background with bare `&` and assume completion. Use
`background:true` + the `process` tool, `& wait`, or serial commands.

Sessions live on the **daemon** (`Angelus.Sessions`), not on the agent, keyed by
aria id as scope: so a backgrounded job survives its agent being reclaimed and
keeps its id across a wake. `figaro kill` reaps an aria's sessions; hibernation
does not.

### Workspaces under a sandbox

Under any sandbox but `local`, `system.cwd` is the box's read-write bind.
A birth keeps a requested cwd only if it is a directory on the daemon's
host; otherwise the aria gets `<workspace_root>/<id>` (0700) from
`[sandbox] workspace_root` in `config.toml`, and with no root the birth is
refused. `local` falls back to the daemon's cwd. A fork copies
`system.cwd`, so a workspace is removed only when no remaining aria names
it; nothing outside the root is ever removed. `[sandbox]` is read at
daemon start: changing it takes a restart, not `outfit reload`.

The box also replaces the daemon's resolved state and config dirs with empty
tmpfs, wherever they resolved from (`FIGARO_STATE_DIR`, `XDG_STATE_HOME`, or
under `HOME`), then binds the workspace, the runtime dir and the bundled
skills back. So no tool reads `<state>/troupe/<name>/`, the node's
intermediate key among it. The NixOS module sets `HOME` and
`FIGARO_STATE_DIR` to its `stateDir`, and the container mounts the host's
state dir at `/var/lib/figaro` under the same module, so the rule holds
there too.

### Troupe keys

`[troupe] name` turns them on. The root is an Ed25519 key held outside every
node (1Password): `op read <ref> | figaro troupe create <name>` derives the
root certificate from it (the same bytes on any machine) and issues this
node's intermediate, a CA for one year that may sign only leaves under
`spiffe://<name>/`. `figaro troupe intermediate <name>` renews it, or issues
one on another node from the same root. The CLI signs; the root never
reaches the daemon or the disk. Under `<state>/troupe/<name>/` sit
`root.crt` and the intermediate and node keys and certificates, 0600.

So that the root stays with the founder, `figaro troupe intermediate <name>
--for <node id> --out <dir>` issues another node's intermediate instead:
`root.crt`, `intermediate.crt` and `intermediate.key`, 0600, into `<dir>`,
which must be a tmpfs unless `--allow-disk`; it prints the fingerprints
(root, intermediate, public key) and nothing secret, and never overwrites.
On a node where the key should not rest on disk, `[troupe]
intermediate_key_credential = "<id>"` reads it from
`$CREDENTIALS_DIRECTORY/<id>` (systemd `LoadCredential=`) and never from
the troupe dir, while `root.crt` and `intermediate.crt` stay there. A
missing credential, or one that is not the key the certificate names, is an
error that closes the node door; a key still at rest beside it is logged.

A node can hold no intermediate and keep even its node key off the disk.
`figaro troupe keygen --out <tmpfs dir>` makes the key there and prints its
public key; it is delivered as a credential and named by `[troupe]
node_key_credential`. On a node that holds an intermediate, `op read <ref> |
figaro troupe leaf <name> --for <node id> --public-key <hex> --door
<host:port> --out <dir>` issues that node a leaf for the key, signs its join
(owner: the caller), has the root name this node its renewer, has the
daemon spread both, and writes `root.crt`, `node.crt` and `joined.json` for
the node's troupe dir: all public, and the private key never travels. The
node renews from its renewer for the same key and writes back only
`node.crt`; a new key means a new credential and a new `troupe leaf`.

The daemon issues its node certificate (seven days, URI SAN
`spiffe://<name>/node/<node id>`) from the intermediate at start and renews
it, with a fresh key, once less than a third of its life is left (checked
hourly); the node door and the dialer take each new one on the next
handshake. With `[troupe] name` set, `[door.tls]` and `[peers.<id>]` may
omit their certificate files and the peer `identity`. An expired or missing
intermediate keeps local callers served, closes the node door and the
dialer, and logs the command to run; `troupe ls` shows both expiries.

### Troupe membership

`[troupe] provider = "static"` (the default) takes the members from
`[peers]`. `provider = "figaro"` takes them from signed entries instead, and
`[peers]` must be empty: a join (signed by the admitting node's
intermediate, naming the node's door and its owner, the principal that
joined it), a leave (signed by the node's own key) or a revoke (signed by
the root). Each is verified at the time it was signed. A leave or revoke
beats every join of its node, and a root revoke also voids the joins its
node's intermediates signed. A node holding an intermediate signs its own
join at start with `[door.tls] advertise` (default `listen`; required when
`listen` is a wildcard), which marks it as able to sign others in.

The entries live in an unbound form the daemon writes (its id in
`<state>/troupe/<name>/ledger.form`); the signatures, not the form, are what
protect them. Nodes trade the whole set over `troupe.entries` on the node
door: on a handshake whose digest differs, when a member first appears, and
hourly. Both TLS verifiers refuse a node outside the set and a chain through
a revoked node's intermediate; a revoke that arrives while links are open
closes them.

A node's certificate counts only when its node's own intermediate issued it,
or a **renewer** the ledger names for that node: a renewer entry, signed by
the root (`op read <ref> | figaro troupe renewer <name> <node> <renewer>`)
or by the node's own intermediate. Both TLS verifiers apply this, and so do
a leave (a leave under a leaf from any other intermediate does not count)
and the old-key proof of a rejoin, and so does a join: once a node has a
join from its own intermediate or a renewer, a join from any other
intermediate no longer sets its door, owner or admitter. So an intermediate
cannot act as, or move, a member that is not its own or its renewee. A node joined through another member
holds a leaf from that member, which the rest of the troupe takes once the
root names the member its renewer; `troupe join` says so. A revoked
renewer renews nothing. The joiner cannot name its renewer itself: the key
it would sign with is certified by the intermediate it would name, so a
rogue intermediate could forge the same statement for any member. That
needs node ids bound to a long-term node key, which UUID ids are not.

`troupe.sign` admits a node: a caller holding `join` on `troupe:<name>`
sends a node id, an Ed25519 public key and a door, and a node holding an
intermediate returns a seven-day leaf, its issuer, the root and the
entries, which now carry the new join. A node holding only a leaf refuses
and names the members that can sign. A node id that is already a member is
signed again only when the request is signed by that node's old key (its
leaf may have expired), so holding `join` does not let anyone act as an
existing node. A joined node renews its leaf the same
way over the node door, as a node principal asking only for itself and only
of its renewers, which needs its current leaf, unexpired and unrevoked; a
member that is not its renewer refuses. Once the leaf expires the node
joins again. `troupe.identity` (also `join`) returns the caller's principal,
the root and its SHA-256, and the signers, for a joiner to check before it
trusts anything.

A join's owner is the caller's principal as `troupe.sign` saw it, so
since issuer scoping it reads `user:jack@auth.kelliher.info`. Joins signed
before that name their owner unscoped; such ledgers exist only in
development roots, and nothing rewrites them.

**The troupe policy** is a ledger entry too: a version, the admin who asked
(`by`), and the document (policy TOML plus optional `[[issuers]]` and
`setters = ["<node id>", ...]`), signed by the root or by a node's
intermediate. Versions are adopted in order: one the root signs always
counts, and the root signs version 1 (`op read <ref> | figaro troupe policy
set <name> <file> --root`); any later version counts only if its signer is a
setter of the version adopted before it. So an intermediate the policy does
not name cannot publish, and a setter dropped from the list cannot return
by writing a higher version. A ledger stores a node's policies only once
some stored version names it, and keeps every version, so each can be
checked against the one before. `troupe.policy.set` checks the document,
refuses explicit grants on `sandbox:local` and admin or every verb on the
troupe, refuses on a node that is not a setter of the version in force,
signs the next version, and spreads it before it returns. Among the
versions that count, the highest version is in force on every node; within
one version the root's entry beats any setter's, and the highest body hash
breaks a tie among the rest; a write that lost a tie and a version that did not count stay
visible to `troupe policy show`. A root revoke of a signer voids its
versions, so the chain continues without them. Passages, answers and `troupe.entries`
replies carry each side's policy version, which `troupe policy show` lists
per member with the age of the sighting. With `provider = "static"`,
`[troupe] policy` names an unsigned file instead, read at start and on a
policy reload. `troupe ls` marks a member quiet after two hours unheard
and lapsed after seven days.

`troupe.leave` (admin on the troupe) has the daemon sign its own leave with its node key;
a root revoke reaches the daemon through `troupe.entries` from a caller
holding admin on the troupe. Either is spread to every member before the
call returns, which says who heard. A door gives an evicted node's open
connections a second to finish the reply in flight, then closes them.

### Troupe api keys

An api key is a credential the troupe issues, for a principal of its own:
`service:<name>@troupe:<troupe>` or `user:<name>@troupe:<troupe>`. The scope
cannot be an issuer's (`troupe:<name>` does not parse as a URL host), so a key
never passes for an issuer's principal, and it carries no groups. Grants come
from policy naming the principal, like any other.

The wire form is `fgk_<id>_<secret>_<crc>`: a 10-character id, 32 random
bytes, and a CRC32 that catches a typo before any lookup. All base32. A node
keeps `sha256("figaro-api-key\0" || id || secret)` and never the key; with a
256-bit secret a slow hash would cost every connection and buy nothing.
`figaro troupe apikey mint` makes the key in the CLI and sends only the id
and the hash, so the key reaches a node only when it is presented.

`troupe.apikey.mint` takes admin on the troupe. A node holding an
intermediate signs an `apikey` ledger entry: the id, the principal, the hash,
`not_after` (required, at most 365 days, 90 by default), the admin who asked
(`by`) and the signing node. A leaf-only node refuses and names the signers.
`troupe.apikey.revoke` is open to the minter, the key's own principal (a
leaked key can revoke itself) or a troupe admin. The serving node signs an
`apikey-revoke` with its leaf. A revoke beats its key in any order, and a root
revoke of the signer voids the keys it signed, as does a second entry under
the same id. Both are spread before the call returns and logged as
structured events (id, principal, by, node). `troupe.apikey.ls` lists every
key to a troupe admin, and otherwise the keys the caller minted or that name
it; it also says who the caller is, which `troupe login` uses. Key entries
have a cap of their own (1024 of each kind), apart from the joins. An expired
key and its revoke leave the ledger a day after neither can matter, and
gossip cannot bring them back.

`authz.APIKeys` holds the live set behind an atomic pointer.
- Opening a key: parse, look up by id, constant-time compare of the hash,
  then `void` and expiry.
- Unknown id, wrong secret and junk all fail with the same error.
- Every RPC re-reads the key from the current set, so an expired or revoked
  key stops at the next call.
- A revoke cancels every connection the key holds, forwarded ones on other
  nodes included, as each node takes the revoke. Each connection also ends at
  `not_after`.
- `[[authz.api_keys]]` (id, principal, sha256, not_after) adds keys from
  local config for that node alone, outranking a ledger entry under the same
  id. `troupe apikey mint --static` prints the key and the stanza.

A troupe file with `[auth] scheme = "api-key"` sends the key as the bearer,
over wss only. It comes from `key_file`, which must be 0600 or tighter and
owned by the caller, or from `troupes/<name>.key`, sealed by hush, which
`troupe login` writes after the door accepts the key. Keys are taken on
`authn = "bearer"` web doors and on the node door. An upstream door does not
take them yet: the edge would have to pass them through.

The web door listens on a unix socket, so it cannot see a caller's source,
and failed presentations are not rate-limited there. Guessing a 256-bit
secret is out of reach; a limit for hygiene belongs at the edge.

## Storage

State root `~/.local/state/figaro/arias/`: parallel XWAL trees in `ir/`,
`form/`, and `translations/<provider>/`, plus `_meta/<id>.json`
for list/status metadata. See arias.md for reading these safely.

`XwalBackend` memoizes one row cache per (aria, channel) so a reader sees the
writer's appends lock-free. Those caches are bounded and evictable; what they
cost and when they are released is [reclamation.md](../contributing/reclamation.md).
