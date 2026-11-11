# Figaro architecture

The durable shape of the system. Conventions drift: when in doubt, read the
source named here and trust it over this file.

## Three roles, one binary

- **CLI** (`internal/cli`): what the user runs. Connects to the daemon's door
  and renders the stream.
- **Angelus** (`internal/angelus`): the long-running supervisor (single
  instance via flock). Owns the registry of figaros, the **endpoint** each figaro is
  reachable at, the daemon-wide session registry, pid bindings, and
  `figaro.list`/`create`/`kill`/`attach`. Survives shells. `figaro rest` stops
  it; [cli.md](../cli.md) says when the next command respawns it.
- **Agent** (`internal/figaro`): one per figaro (= one conversation), and
  **transient**. Owns the figLog (IR), the form, the tool registry, and
  the turn loop. Mutations funnel through its **inbox** (an event queue), so
  there is exactly one writer to the form and the log: e.g. a `figaro
  set` arriving mid-turn is serialized, not raced. Form *reads* need no
  inbox: snapshots are immutable and published atomically (see the form
  section).

An agent is a memory decision, not an identity. It is built on demand and
reclaimed when idle, while the figaro's endpoint, bindings and background jobs
outlive it: so "the figaro" and "the agent serving it" are different lifetimes.
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

Per-figaro key→JSON state. Two namespaces:

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
  does not even mark the bound form dirty.
- `Diff` (`treediff.go`) is a merge-join by key with a **pointer-identity
  short-circuit**: `prev == next` proves a whole subtree is unchanged, so a
  one-key delta costs ~10 node comparisons on a 1024-key bound form. The
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
  bound form: `cur.Apply(candidate).Diff(cur)`.
- `MarshalJSON`/`UnmarshalJSON` emit and read the **flat object** form
  (`{"key": value, …}`, keys lexical). Three things consume it: the reducible
  form channel's watermark/state records in the figaro store (see
  [store.md](store.md): those are content-hashed), the `figaro.form` RPC
  response, and `State.Save`'s the form channel when a State is opened with a
  path (the agent opens with `""`, i.e. in-memory only). `MarshalJSON`
  delegates to `encoding/json` over a `map[string]json.RawMessage` and never
  hand-rolls the object: `encoding/json`
  compacts a raw message and rewrites `<`, `>`, `&` as `\u003c`, `\u003e`,
  `\u0026`, and that post-processing is part of the bytes already on disk.
- **The custom codec is charged twice.** `encoding/json` re-scans a marshaler's
  output and pre-scans an unmarshaler's input: ~2x on a 15KB bound form, for
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
bound form. (Before that, `State.snapshot` was a plain field read with no
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

Per-figaro request methods: `figaro.qua` (prompt), `figaro.context`,
`figaro.interrupt`, `figaro.set`, `figaro.form`,
`figaro.queued`, and `figaro.read` (catch-up/paging). Angelus includes
`figaro.create`/`fork`/`promote`/`kill`/`list`/`attach`,
`pid.bind`/`resolve`/`unbind`, `aria.read`, `aria.page`/`context`/`form`
(the same reads addressed by figaro id), and status/binding persistence.

`aria.read` answers one page, capped at 1000 entries whatever the limit asks,
and names the next LT in `next_from`. Anything that wants the whole figaro
(`export`, the CLI-side turn resolution behind `attend <id>:N` and `status`)
goes through
`sdk.Angelus.IRAll`, which walks `next_from` to the end; a single call there
is issue #23 again.

The transport is NDJSON-framed JSON-RPC 2.0 over one door per daemon. A figaro
method names its figaro in `params.figaro_id`. A connection follows a figaro
after `figaro.follow` or a prompt there; reads never subscribe. Follow, call
`figaro.read` for initial state, then keep reading notifications, each of which
names its figaro.

Figaro methods are served by the **angelus**, not by the agent, and answer
whether or not an agent is resident: `rpc.MethodNeedsAgent` decides which
methods are served from the store and which wake the figaro. See
[reclamation.md](../contributing/reclamation.md).

### Caller identity: `x-internal-figaro-id`

Every request carries the **calling figaro's** id in a reserved params field
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
`--id > FIGARO_ARIA > pid binding` and answers *which figaro am I talking about*;
caller identity answers *which figaro am I*, and only `FIGARO_ARIA` can: `--id`
is an argument the caller chose, and a pid binding says which figaro a shell is
*attending*, not that it is one.

### Attribution: who sent each part of a message

A user message is **not always one submission**. Consecutive prompts drain and
fold into one message, and they may come from different callers. `Content` and
`livedoc.Node` therefore carry a **`Sender`**, and `aria.Turn` carries
**`InquirySegments`**: the opening question split by who asked it.

`Turn.Inquiry` is unchanged and remains the canonical *text* (search, selection
hashing, the mantra seed and height estimation all want one string); the
segments are the attribution layer over it.

What the sender is recorded as:

| caller | `Sender` |
|---|---|
| authenticated figaro | its id |
| **the duke** (end user) | the target figaro's `duke-title`, else `user` |
| explicit label (`FIGARO_CALLER`) | bare, e.g. `ci-bot` |
| unknown | empty |

The pager draws every sender (`figaro <handle>` for a figaro). The model sees
only a figaro's: every provider puts a
`<system-reminder name="sender">figaro <handle> (<short id>)</system-reminder>`
in the same user message, just before that figaro's text, through the channel
form-state reminders use. It is built from the id alone, so the bytes never
change between renders, and the text itself is sent as typed. The duke's and a
label's prompts go to the model as typed: recorded history cannot tell a label
from a `duke-title`, and both name the owner's side.

**The duke is the end user**: the person the agent serves, as distinct from an
figaro or an anonymous script. Their name does not live in shell config: an
*interactive* CLI sends a **placeholder** in `x-caller`, and the agent resolves
it against the **target figaro's** form key **`duke-title`** (default
`user`). Set it in an outfit:

```toml
duke-title = "gluck"
```

`x-caller` is a **typed ref**, not a string, and that is a security property
rather than tidiness: `FIGARO_CALLER` only ever populates `label`, so no value
of it can produce `duke: true`. The guarantee is a type, not a reserved word.

Only an **interactive** process presents the duke: the same TTY signal that
decides the binding policy: so a figaro's shell-out cannot speak as its master
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
| web door (`[door.web]`) | per upgrade request, `authn = "upstream"` or `"bearer"`; any other value and the door does not start | upstream: the edge's `Remote-User` / `Remote-Groups`, scoped by `[authz.upstream] issuer`, trusted only on that socket and never on the CLI door's path. They are read once, at the upgrade, so the connection ends at `[door] edge_follow_max_age` (default 1h) with a `door.lapsed` notice and the reason in the close frame; the pager dials the edge again and follows on, judged on the groups the edge names then. `[door.web] max_conn_age` (default 8h) ends any web connection the same way. bearer: a session bound at the upgrade, refused once the token expires; the client reconnects. A bearer starting `fgk_` is an api key (Troupe api keys, below) and goes only to the key table; anything else goes only to the issuers. |
| box door (`[sandbox.boxes.<name>] door`) | `authz.BoxAdmit`: every caller is the box, whatever its uid; seated only if the policy binds it | `sandbox:<name>@local:box`. `local:box` is not a host, so no issuer or api key can mint it. |
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
`aria:<id>` when the request names a figaro and on the daemon otherwise.
`figaro.hello` is exempt; `figaro.list` is served and drops the rows the
caller cannot read before it pages, and `node.resolve` answers only the refs
the caller may read. A call that reaches a second resource
checks it too: `study` reads its form, `cast` sets its role, `lineage`
reads `against`, `kill -r` needs `kill` on every figaro it takes. A prompt or
`figaro.follow` subscribes the connection only where the caller holds
`listen`. Denials are JSON-RPC **-32020** with the reason verbatim. A request
for a figaro another node holds is forwarded before this node's policy runs,
and the owner decides it (the node door, under "JSON-RPC surface" in
`ARCHITECTURE.md`).

Ids are resolved before the policy sees them, and a prefix resolves only
among figaros the caller may read, so resolution cannot list or expand an id
the caller could not read.

**The policy** is TOML at `[authz] policy` (relative to the config dir):
roles of permissions (`[roles.<name>] permissions = [...]`), bound to
principals by `[[bind]]`. A permission is `verb:selector`
(`send:role:@herald`, `read:owner:self`, `*:*`); the verb ends at the first
colon, and either side may be `*`. Selectors are `aria:<id>`, `role:@<name>`,
`owner:self`, `lineage:<id>`, `sandbox:<name>`, `sandboxed:self`, `unboxed`
and `troupe:<name>`. A role's permissions are kept canonical: duplicates and
any another covers are dropped, then sorted by verb and selector. The
`{ verbs, on }` tables of 0.59 and earlier are refused, and the error spells them as
strings. `grants` is the old name for `permissions` (and for the `[figaros]`
ceiling's key): 0.64 still loads it, logs one warning per load naming the
new key, and refuses a table that sets both; the next release drops it. The
troupe policy reads the same names. With none
configured, the daemon's own principal holds every verb and nobody else holds
any. `outfit reload` and `angelus.configure` reload it; a file that fails to
load leaves the previous policy in force. `owner:self` reads `minted_by`,
which the birth record writes once and nothing rewrites. `unboxed` is every
figaro whose `system.sandbox` is exactly empty or `local`: any other value is a
box (a configured box, one since removed from config, the removed `bwrap`), and a form
the store cannot read is a box too. Lent to `troupe:<name>`, it keeps a
troupe's reach off the node's boxes.

**Moves and consent.** Promote, demote, `@topology` and placement are plans
the topology guard checks: `kill` on every node whose position changes, and
`adopt` on every aria that receives a node, unless that aria and the moved
node have one owner, or the node goes back under its history parent. Without
`adopt` the move parks on that parent as a proposal (`figaro adopt <parent>`
lists them). Someone with `adopt` there accepts or refuses it. Accept checks
the move again as its proposer, with the grants the proposer holds then. A
move that needs two parents' consent is refused. Proposals live in memory:
they lapse after a day, a restart drops them, and each parent holds 16.
`adopt` must stay out of a troupe policy until every member runs a release
that knows the verb.

**Sandboxes.** `use` on `sandbox:<name>` is checked on the dressed form at
create, fork, bind, import and `figaro.set`. A figaro minted by a sandbox
principal is pinned to that sandbox; a set, a fork's patch or a cast's role
patch that names a sandbox the caller holds no `use` on is refused, so a box
cannot move or mint anything out of its box. `form.create` and `form.bind` with a
parent need `read` on it, since the new form starts as its copy.

**Box principals.** With no policy file, each box that has a door is bound
to `create`, `use` on `sandbox:self` (its own box) and every figaro verb but
`admin` on `sandboxed:self`: the figaros and forms whose `system.sandbox` is that box.
Figaros in one box share its files and processes, so the box is the unit a
policy can separate; a figaro moved out of the box leaves the box's reach.
A policy file binds boxes itself, with the same two selectors. A box
writes only the `system.` keys `boxWritable` lists (`internal/angelus/box_keys.go`):
sampling knobs, tags, cwd, sandbox, the decision provider and model. Among those it may not write, the daemon dereferences
`system.environment.*` (a directory it writes wire logs to),
`system.decide.base_url` (a URL it sends a credential to), `system.methods`
(keys it writes for the figaro), `system.studies` (forms it reads) and
`system.reaped(_until)`. Open keys are its own except `target-aria`, and a
test fails on any open key the daemon reads that no one has classified. That holds for set, prompt, cast, create, fork and
form.create/bind; values from outfits it names are the owner's. A box may not
import, and the cwd it sends at birth is never looked up on the host. An
unbound form a box mints, by `form.create` or by `cast` with a role patch, is
held in that box (`system.sandbox` stamped by the daemon, which the box cannot
change), at most 256 per box. A role held in a box points only at a figaro in
it: `set`, `cast` and `form.create` refuse any other `target-aria`, the CLI
refuses to follow one, and `form ls`, `ls` and `status` name the box. Shell
bindings stay admin-only, since they are keyed by a pid and a box has its own
pid namespace; `figaro new` inside a box prints the id instead of attending. Under `local` a `system.cwd`
the caller names is a host path and needs `use` on `sandbox:local`.

**Owner-held keys.** `system.provider`, `system.model`, `system.max_tokens`,
`system.max_context_tokens`, `system.credo`, `system.tools`,
`system.methods`, `system.environment` (where wire dumps go),
`system.decide.base_url` (where a provider credential goes), and
`system.ttl`, `system.reap_if_empty`, `system.keepalive`,
`system.retired` and `system.retirement` (when the sweep deletes a node and its subtree)
(`api/form/owner_held.go`) are set or removed only by the
owner: the local door, an in-process call, or the owner's principal. Every
other caller, a box included, is refused at set, prompt, cast, create, fork
and form.create/bind. The one exception is `system.retired` for a box, on
the figaros in that box, so `figaro retire` works inside one. `system.tools` lists the tools a figaro may use; `[]` is
none, unset is all, and a value that is not a list is none. It is enforced
where tools are offered to the model and again where a call runs, so a
forged call to an unlisted tool is refused. Owner-held does not bind an
unboxed figaro whose tools include bash or process: those tools reach the
local door as the owner (issue #57).

**Figaro scopes.** `system.scopes` narrows what one figaro may do. It is a
protected key: the owner writes it, or an ancestor figaro through its box
door, and a figaro gives only what its own effective set covers. Its value
is a list of `verb:selector` entries in the policy syntax. A figaro's effective set is the
policy's `[figaros]` ceiling, intersected with `system.scopes` of each aria on
its lineage, top first. An aria that sets none narrows nothing for figaro's
verbs and holds none of anyone else's: `egress:`, `cred:` and any verb figaro
does not know reach a figaro only when every aria on its lineage writes them,
and the ceiling, when set, allows them. A figaro with no scopes anywhere holds
no such verb. In a set, `aria:self` and `lineage:self`
mean the aria the set is written on. A call that names a figaro this node holds is
allowed only when both its principal and the figaro's effective set allow
it; a call naming no figaro, or one held elsewhere, is decided by its
principal. `figaro scopes <id>` shows each level, the ceiling and the
effective set, and so does `figaro.scopes`.

`system.grants` is the key's old name. 0.64 reads it when `system.scopes` is
absent, logging one warning per aria per daemon run; an aria carrying both
holds nothing; no caller may write `system.grants`, though removing it is
allowed to those who may write `system.scopes`. `figaro set system.scopes`
drops `system.grants` in the same write, an import renames it, and a figaro
leaving its ancestors writes the new key. `figaro grants` and `figaro.grants`
are the old names of the command and method; each level carries its set under
both `scopes` and `grants`.

A provider must read the effective set from `figaro.scopes`, not the raw key:
the key holds only that aria's own narrowing. A `*` verb means figaro's verbs
only, never `egress:` or `cred:`, whoever writes it; so that no reader takes it
for more, `figaro.scopes` spells every `*` verb out as figaro's verbs, one
entry each. A value that does not parse is
refused on write, and one stored before holds nothing. A figaro that moves to
another node writes its effective set into its own key first, so leaving its
ancestors behind does not widen it.

```toml
[figaros]
permissions = ["read:*", "send:*", "*:lineage:self", "egress:*"]
```

The claim is the CLI's stamp (`FIGARO_ARIA`), so a tool can drop it and be
judged by its principal alone: the owner on the local door, the box in a box.
A claim only ever narrows, so a forged one gains nothing. On the box door a
claim counts only for a figaro that runs in that box; any other (an unknown
id, another box's figaro, an unboxed one) is dropped and the call is judged
as the box. `[sandbox.boxes.<name>] figaro_claim = "required"` refuses those
calls instead, and any call through that box's door that names no figaro.

A refused read hides the aria as one the caller cannot read (not found, and
dropped from `ls` and prefix resolution); other refusals are -32020 naming the
figaro and its effective set. Each refusal at the guard logs one
`authz: figaro refused` line at info in the daemon's log, with the figaro,
method, verb and resource; allowed calls log nothing. `[[fence]]` tables are
gone: a policy that still carries one is refused at load. A fence that denied
every figaro is the `[figaros]` ceiling without those permissions; one that let
only some figaros in is the ceiling without them plus those figaros' own
`system.scopes`.

**The credo is fixed at birth.** `system.credo` is the system block, so a
change rewrites the head of every request and drops the prompt cache. It is
set by the outfit or the birth patch of `new`, `fork -S` or `succeed`; once a
figaro has a message that is not ceremonial, a `set`, `unset`, `state
outfit` or a prompt's `-O`/`-S` that would change it is refused, for the
owner too
(`internal/angelus/credo_birth.go`). The same value again is not a change.

**Box providers.** `[sandbox.boxes.<name>] providers = [...]` names the
providers a figaro in that box may run on. Anything else is refused where the
provider is resolved: at birth, at wake, on a rebind, and for a decision
asked by the box or for a figaro in it. A box with a door must set it; see
below.

**Box outfits.** `[sandbox.boxes.<name>] outfits = [...]` names the outfits
a box may dress in. A box naming any other outfit is refused at create, fork,
set, prompt, cast and form.create/bind. Whatever mints a figaro from the box
names a listed outfit, or is refused with the list: create and form.bind
always, and a fork unless it is a head fork of a conversation, which carries
its parent's dress. No implicit default. The owner is never limited by it.

**A door needs both lists.** A box with a `door` and no `outfits` or no
`providers` list gets no door: the daemon logs "box door refused" naming the
box and the missing list, and opens the other doors. A list of exactly
`["*"]` allows any outfit or any provider, logged when the door opens; `"*"`
beside names is a config error. A box without a door needs neither.

**Host doors.** `[sandbox.boxes.<name>] host_door = "<path>"` opens a second
socket for a doored box, for services on the host (a chat backend, a receipt
worker) rather than processes in the box. The daemon tells the two apart by
the socket a connection came through, never by what the caller says. A
host-door caller is the box's principal, `sandbox:<name>@local:box`, so it is
exactly as strong as the box: every grant the box holds, it holds. The box's
`figaro_claim = "required"` binds only its in-box door; host-door calls are
answered without a claim, and a claim on one counts by the same rule as in
the box. The host door must sit outside every box's door directory, since a
box reaches its own; the daemon refuses one inside any of them.

**Box purge grace.** `[sandbox.boxes.<name>] purge_grace = "1h"` is how long
a reaped or killed figaro in that box waits before it is purged, in place of
`[memory] reaped_grace_hours` and `kill_grace_hours`. A kill asking for a
longer grace gets the box's; `kill --now` still purges at once.

**Troupes.** `join` on `troupe:<name>` is checked by `troupe.sign` and
`troupe.identity` themselves, and `admin` on it by `troupe.entries` (from a
caller), `troupe.leave`, `troupe.policy.set` and `troupe.apikey.mint`, since
the guard knows only figaros and the daemon. `troupe.apikey.revoke` and
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
own unless `[authz] troupe_issuers = false`: https only, `clients` resolve
only to `user` or `service`, never one of the node's own URLs or scopes, and
never trusted for the edge's reverify.

**Kind wildcards.** jkauthz matches `<kind>:*` on kind alone; the issuer
scope is part of the id, so `user:*` admits users from every issuer, troupe
issuer and api key. A troupe policy may not bind `user:*`, `service:*` or
`sandbox:*`; a node's own policy may, and each is logged at load. `node:*`
is allowed in both: node principals come only from mTLS peers and from a
node's own issuers. A version adopted from the ledger passes the same rules
as one set at a door; one that breaks them (the root may sign anything)
grants nothing on the node and its issuers are not taken up. The real fix
is in jkauthz: a scoped wildcard (`user:*@auth.kelliher.info`) that a troupe
policy could bind, after which the bare forms can be refused everywhere.

`x-caller` is **attribution only and never authorizes**. It lands in
`Identity.Label`, a different field from `FigaroID`, so an assertion naming
its own target cannot impersonate it. The stamped principal rides beside it
on every prompt block (`message.Content.Principal`), so a transcript says who
spoke as well as who claimed to.

A figaro may fork itself from inside its own running turn. The fork handler
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

Presentation is a pure client concern. M-m shows more (other name spellings, node addresses);
Ctrl-T enters or leaves the transcript. Thinking is muted by default, tools are
native widgets, and spinners animate locally. The wire carries semantic node
data rather than terminal rows, ANSI, width, theme, or animation ticks.

## Provider layer: `internal/provider/anthropic`

Direct HTTP+SSE, no SDK. Translates IR <-> Anthropic wire and caches the
per-figaro wire bytes (`store.Log[[]json.RawMessage]`, keyed by figaro LT).

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
  with no content, the figaro went quiet, and since the trigger stayed in
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

### Wire dumps: `system.environment.figaro_wire_dir`

Set on a figaro, it makes every provider (messages, chat, responses) write each
call as `<dir>/<figaro>/<ns>.req.http` and `.resp.http`: the request line,
headers and body, then the status, headers and the stream (one frame per line
for responses). Files are created `0600` in a directory held at `0700`.
Credentials are masked by name, in headers and query parameters alike: any
name containing `auth`, `key`, `token` or `secret`, and cookies.
`Authorization` keeps its scheme. The body is not masked. It is the whole
conversation, tool output included, so a secret that entered the context is
written in clear: a second copy outside the store, which nothing cleans up.
The key is owner-held.

### The provider binding is live, not a birthmark

`system.provider` is form state like any other key, and the bound form is
authoritative. The agent holds a `ProviderFactory` and re-resolves the binding
at the top of **every** provider round (`internal/figaro/provbind.go`,
`syncProvider`), after that round's queued `set`s are serviced: so
`figaro set system.provider copilot` (or a re-applied outfit that moves the
figaro) takes effect on the next round, with no restart and no fork. Before
this, the instance was frozen at create/restore: a figaro whose provider was
wedged (a persistent `overloaded_error`, say) could not be moved off it while
the angelus stayed up, and `figaro status` cheerfully reported the *old*
provider while the bound form said otherwise.

- The binding is `{name, knobs, instance}` published through an
  `atomic.Pointer`: written by the drain loop, read lock-free by
  status/metrics on RPC goroutines.
- **Model is not a rebuild trigger.** Every provider resolves `system.model`
  from the per-turn snapshot inside `Send`; rebuilding on a model change would
  only discard the in-memory wire projection. Build-time knobs
  (`system.reminder_renderer`, `system.max_tokens`)
  do trigger one.
- A factory error **fails the turn** naming the provider. Falling back to the
  old instance would silently contradict the bound form: the exact confusion the
  bug produced.
- Switching is safe because the IR is canonical and each provider owns its
  own translation channel: the new provider simply re-projects the history it
  has not seen. That makes the cache-miss encoder load-bearing: it must drop
  unsigned thinking blocks (both the SDK and raw Anthropic encoders do).

### Credentials belong to hush, not to a figaro: `internal/auth`

A provider instance is per-figaro, so anything cached on one is cached N
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
figaro id as scope: so a backgrounded job survives its agent being reclaimed and
keeps its id across a wake. `figaro kill` reaps a figaro's sessions; hibernation
does not.

### Boxes: `internal/sandbox/box`

A box is a unit (an nspawn container, a VM) the daemon reaches only over a
unix socket. `figaro box serve` runs inside it and serves `api/sandbox` from
the `local` provider over jkrpc: `box.open` (scope = figaro id, workspace,
env), `box.exec` and `box.session.*` with output as `box.chunk` and
`box.exit` notifications keyed by a handle the caller picks, and
`box.fs.*`, where an update is a compare-and-swap on the old bytes'
sha256. It reads no config and writes no state.

```toml
[sandbox.boxes.judge]
exec    = "/run/figaro/boxes/judge/exec.sock"   # box serve --socket
door    = "/run/figaro/boxes/judge/door.sock"   # optional: the box's way back
profile = "build"                                # carried, not yet read
```

`system.sandbox = "judge"` opens it. The box owns the directory its exec
socket lives in, so the daemon opens `exec` with openat2 refusing a link
in any component (`O_PATH|O_NOFOLLOW` on the last one where openat2 is
missing), refuses anything but a socket, and connects through
`/proc/self/fd`: a link there cannot point it at another socket. So a
configured `exec` path must contain no symlink at all, a legitimate one
included (`/var/run/...` is refused; write `/run/...`). Paths are paths in the box: a box
figaro is born without the CLI's host cwd, `system.cwd` (if dressed)
resolves against the box's `--workspace`, and none of it needs `use` on
`sandbox:local`. Cancelling an exec or killing a session signals its
process group in the box. When the socket drops, everything started over
it ends with exit -1 and a line saying so; the next call redials, and
sessions are found again. A dropped connection kills its execs in the box,
not its sessions; `figaro kill` kills those. Isolation is what the box
server claims, `local`'s, named `box`: the server cannot vouch for the unit.

The daemon listens on each `door` (0660) at start; `box serve --door
<path>` gives every command `FIGARO_TROUPE=unix://<path>` and
`FIGARO_RUNTIME_DIR`, so `figaro` in the box calls back as the box and
never starts a daemon there.

### Removed: bwrap

`system.sandbox = "bwrap"` named a bubblewrap provider on the daemon's
host. It is gone: such a figaro refuses to open its sandbox until
`system.sandbox` is `local` or a box. Under `local` an unusable
`system.cwd` falls back to the daemon's cwd.

### Troupe keys

**Federation status: in development.** Solid: troupe keys, membership (join, leave, revoke,
gossip) and the cross-node listing (`ls`, the pager's `L`). Not yet settled:
moves across nodes, what one node answers about a figaro held on another
(moving, moved, owner down), and troupe policy v2 (binding principals other
than the owner).

`[troupe] name` turns them on. The root is an Ed25519 key held outside every
node. Every command that takes it reads it on stdin and never writes it; it
lives in hush as the secret of a `troupe-root` command whose script pipes it
into `figaro troupe "$@"`, so `hush troupe-root create <name>` derives the
root certificate from it (the same bytes on any machine) and issues this
node's intermediate, a CA for one year that may sign only leaves under
`spiffe://<name>/`. `hush troupe-root intermediate <name>` renews it, or issues
one on another node from the same root. The CLI signs; the root never
reaches the daemon or the disk. Under `<state>/troupe/<name>/` sit
`root.crt` and the intermediate and node keys and certificates, 0600.

So that the root stays with the founder, `hush troupe-root intermediate
<name> --for <node id> --out <dir>` issues another node's intermediate instead:
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
node_key_credential`. On a node that holds an intermediate, `hush troupe-root
leaf <name> --for <node id> --public-key <hex> --door
<host:port> --out <dir>` issues that node a leaf for the key, signs its join
(owner: the caller), has the root name this node its renewer, has the
daemon spread both, and writes `root.crt`, `node.crt` and `joined.json` for
the node's troupe dir: all public, and the private key never travels. The
node renews from its renewer for the same key and writes back only
`node.crt`; a new key means a new credential and a new `troupe leaf`.

The daemon issues its node certificate (URI SAN
`spiffe://<name>/node/<node id>`) from the intermediate at start and renews
it, with a fresh key, once less than a third of its life is left (checked
hourly). Its life is `[troupe] leaf_ttl` (default `7d`, one day to ten
years), never past the issuer's `[troupe] max_leaf_ttl` (default `30d`) or
the intermediate's end; a leaf-only node sends its `leaf_ttl` to its
renewer, which grants the same way, and a 30-day leaf renews from day 20.
A longer leaf trades revocation lag for fewer renewals: it matters only for
a stolen node key nobody revokes, since a revoked node or intermediate is
refused whatever its leaf says; the node door and the dialer take each new one on the next
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
the root (`hush troupe-root renewer <name> <node> <renewer>`)
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
intermediate returns a leaf (the `ttl_ms` asked, seven days if none, at
least a day, under its `max_leaf_ttl`; an ask past that is granted the most
and logged at Warn once a day per node, and `ttl_ms` in the reply says what
was granted, which `troupe join` reports), its issuer, the root and the
entries, which now carry the new join. A node holding only a leaf refuses
and names the members that can sign. A node id that is already a member is
signed again only when the request is signed by that node's old key (its
leaf may have expired), so holding `join` does not let anyone act as an
existing node. A joined node renews its leaf the same
way over the node door, as a node principal asking only for itself and only
of its renewers, which needs its current leaf, unexpired and unrevoked; a
member that is not its renewer refuses. A renewal from a new door signs a
join carrying the owner from the node's vouched joins (its own
intermediate's or a renewer's), and is refused when it has none, so a
renewer never repeats an owner another intermediate wrote. Once the leaf
expires the node joins again. `troupe.identity` (also `join`) returns the caller's principal,
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
counts, and the root signs version 1 (`hush troupe-root policy set
<name> <file> --root`); any later version counts only if its signer is a
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
and lapsed once unheard longer than the life of the leaf it last presented
(either side of a handshake records it), or this node's `max_leaf_ttl` when
none was seen.

`troupe.leave` (admin on the troupe) has the daemon sign its own leave with its node key;
a root revoke reaches the daemon through `troupe.entries` from a caller
holding admin on the troupe. Either is spread to every member before the
call returns, which says who heard. A member that already holds the leave,
having learned it by gossip, lets the leaving node through the handshake
only to answer that it holds the leave (no ledger version, no call), and
the leaving node counts it as heard. A door gives an evicted node's open
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
`not_after` (required, at most 365 days, 90 by default), the nodes the key is
good on, the admin who asked (`by`) and the signing node. A leaf-only node
refuses and names the signers. The nodes are a sorted list of node ids, or
`*` for all of them; the minting node alone unless `--nodes` names others.
Mint refuses `*` while any member is leaf-only, naming those members, so a
key reaches a leaf-only box only when its minter names it.
A key counts only when the root signs it or its signer is a setter in the
policy version in force, the rule policy versions follow: troupe policy
grants a key's principal by name, so a key from any other intermediate would
carry those grants. A node that is not a setter refuses to mint, and a key
whose signer stops being a setter stops counting.
`troupe.apikey.revoke` is open to the minter, the key's own principal (a
leaked key can revoke itself) or a troupe admin. The serving node signs an
`apikey-revoke` with its leaf, and any member's leaf will do: a rogue
member can revoke keys, which denies service and grants nothing. A revoke beats its key in any order, and a root
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
  then `void`, expiry, and that this node is among the key's nodes.
- Unknown id, wrong secret and junk all fail with the same error.
- Every RPC re-reads the key from the current set, so an expired or revoked
  key stops at the next call.
- A node never forwards a key's call to a node outside its audience
  (`authz.ForwardableTo`, checked where a peer link opens), and a node door
  refuses a forwarded key that does not name it. A key never reaches a node
  it was not minted for.
- A revoke cancels every connection the key holds, forwarded ones on other
  nodes included, as each node takes the revoke. Each connection also ends at
  `not_after`.
- `[[authz.api_keys]]` (id, principal, sha256, not_after, and optionally
  nodes, default this node) adds keys from local config for that node alone,
  outranking a ledger entry under the same id. `troupe apikey mint --static`
  prints the key and the stanza.

How fast a revoke lands. The serving node refuses the key at once, and
spreads the revoke to every member it reaches before the call returns; the
reply names any it missed. A member unreachable then still admits the key
until it hears, at its next exchange with any member: a handshake whose
ledger digest differs, or the hourly gossip round. The bound is the gossip
interval, one hour, after the member is reachable again; a key's `not_after`
bounds a member that never returns. Keep everyday keys short (days or weeks)
for this reason, well under the 365-day ceiling.

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
for list/status metadata. See store.md for reading these safely.

`XwalBackend` memoizes one row cache per (figaro, channel) so a reader sees the
writer's appends lock-free. Those caches are bounded and evictable; what they
cost and when they are released is [reclamation.md](../contributing/reclamation.md).
