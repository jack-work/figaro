# Aria log: the primitive

Lane C, step 4 of the work order (`docs/proposals/aria-log/README.md`,
revision 3, keel 1065245e6). Branch `lane/aria-log`. What shipped, every
JSON-RPC method it changed, its branch in every switch over node kind, and
what is left to compose with lanes that have not landed.

## What an aria log is

An aria whose bound form carries `system.kind = "log"`. It has no provider,
no model, no sandbox and no tools. A send appends one entry and seals the
turn at once; nothing answers. Everything else is the aria it already is:
the same IR channel through the same journal (`xwal`, synced before the entry
is announced), the same form, hub, fork, kill, gc, ls, study and cast.

An entry is the turn's inquiry: an IR input message whose content blocks
carry the sender's name (`sender`) and principal (`principal`), stamped with
the time (`timestamp`) and the turn (`turn_id`). A citation is
`<log>:<turn>[.<node>]`, the coordinate send and fork already take. Entries
are immutable: no method edits or deletes a turn. Order is the IR's LT order;
a log's inbox never folds queued sends together, so N sends are N entries,
each sender's in the order it sent them.

A send to a log returns once its entry is appended (or with the error that
stopped it), so the ack is the durability. Every accepted prompt carries a key
that rides onto its IR message (`accepted`); after a crash, redelivery drops a
queued prompt whose key is already in the IR, so an entry is never appended
twice. A log marks no turn cut, so a restart never resumes it.

## The kind

`log` is named at three levels, one source:

- **Form**: `system.kind = "log"`, registered (self-written, shape: one of
  `log`). The bound form is the source of truth.
- **Sidecar**: `AriaMeta.kind`, mirrored from the form at birth and on every
  write by the same form-to-sidecar mirror that carries `system.ttl`, so a
  listing learns the kind without opening a form (invariant 15).
- **Listing**: `FigaroInfoResponse.kind = "log"` in every listing (`figaro
  status -j`, `ls -j`), and `turn_count` is the entry count.

The store's node kind stays `conversation`: a log is a trunk with an IR
channel, forkable, placeable and charged like any aria. Introducing a fourth
store node kind would put a log branch into every switch below for no
behaviour that differs; where the log differs, the branch reads the kind from
the form or the sidecar.

## Rules

Two log rules hold for every writer: any caller, the owner, a box, and the
daemon itself. They live once (`form.CheckKind`) and are enforced where every
write lands: the form writer's judge (`store.Form`), and every birth
(`ForkWith`, `CreateForm`). So an outfit's keys folded in by `set`/`send -O`,
and daemon writes (seat, succession, lifetime clamps), meet them as a
caller's patch does. Lane A's `judgeWrite` needs no log rule; its
equivalence golden gains log rows that end in the store's refusal.

1. `system.kind` is fixed at birth: a write to an existing node's form may not
   add, change or remove it. Another kind is another birth (`fork -S`/`-D`).
2. A log carries no `system.provider` and no `system.model`, at birth or after.
   A fork of a figaro into a log drops both (`-D`); `figaro.create` refuses a
   log birth, since its outfit dresses a model; `figaro.models.pick` is
   refused through the set it routes to.

Two verbs refuse on a log, in the aria method chain beside the credo check:
`figaro.interrupt` (a send appends at once; there is no turn) and a
`figaro.qua` with no text and no image (`figaro set` writes the form alone).

## Minting and fan-out

`figaro new --log [-S k=v]... [-- <first entry>]` adds `system.kind=log` to
the birth patch and mints through `form.bind` from the null root: no outfit,
no provider, dormant until the first send. `form.bind` by a figaro now holds
and charges the minter's `max_children` and quota and places the new aria
under it, exactly as `figaro.create` does (`holdBirth`, a birth quota of 0,
`place`); the owner, a box, and an unclaimed caller mint uncharged as before.
This applies to every `form.bind` by a figaro, not only logs: it closed a
path that minted around the fan-out budget. Named break: a figaro's `fig bind
@form` now counts against its `max_children`, the child is born with
`system.fanout.quota = 0` (it can neither fork nor mint until given budget),
and it is drawn under the minter instead of under its form.

## JSON-RPC methods

No method is new, so lane A's action table gains no row.

| method | change | why |
|---|---|---|
| `figaro.qua` | on a log, appends the text and images as one entry, ends the turn `logged`; refused with neither | the send of a log is an append; the turn without a model is the generalization |
| `figaro.read` | new `until` anchor, a forward ceiling | range reads; `[lo, hi)` reads the same forward (`at=lo, until=hi`) as backward (`at=hi, floor=lo`); turns past it are never composed; gated by capability `read.until` |
| `figaro.interrupt` | refused on a log | no turn to interrupt |
| `figaro.set` | refused on the two log rules | kind fixed at birth; no model on a log |
| `figaro.models.pick` | refused on a log via set | a log has no model |
| `figaro.create` | refuses a log birth | it dresses a model |
| `form.bind` | charges a figaro minter | a log mint is a child like any other (ruling 2) |
| `figaro.list` | `kind = "log"`, `turn_count` | ruling 9; read from the sidecar |

Unchanged and serving logs as they are: `figaro.fork`, `figaro.follow`
(listen), `figaro.read` backward, `figaro.ir` (raw entries by LT range),
`figaro.form`, `figaro.context`, `figaro.study`, `figaro.cast`,
`figaro.kill`, `figaro.decide` (a decision model asked about the log; it
never writes to it).

## Permissions

Append is `send` on the log, read is `read`, following it is `listen`: the
grants an aria already has (tested both ways). Mint is `mint`, charged as
above.

## Every switch over node kind, and the log's branch

A log's node kind is `conversation`, so each switch takes its conversation
branch. The column says what that means and whether a log should differ.

| switch | decides | log branch |
|---|---|---|
| `store.NodeLink` (xwal_store.go) | kind of an id: null, outfit stump, trunk | trunk, `conversation` |
| `trunks.IsStump` (xwal_store.go, NodeLink, view) | outfit stumps | never: a log is a trunk |
| `Backend.MatchID` (resolve_id.go, names.go, roles.go) | id prefix resolution | resolves like any aria; no kind switch |
| `chargesQuota`, `fanoutNodes` (fanout_cache.go) | which nodes carry a fan-out quota | charged; a log is a fan-out node and a minted log counts as the minter's child |
| `fanoutLimit` (topology_guard.go) | null and stumps have no limit | has its own limit like any aria |
| P3 fan-out cache: `fanoutNodeOf`, `holdBirth` | live count, budget | counted; `form.bind` now takes the hold |
| S1 normalize plan (lane/auto-normalize-v077, not on this base) | auto-detach of inherited prefixes when quiet | a log minted by `form.bind` from null has no inherited prefix, so S1 has nothing to detach; a forked log is detached like any fork. Quiet trigger: `joined` counts every non-read method as work; on composing, `figaro.qua` to a log (sidecar kind `log`) must not count |
| `hierarchy_engine.Node` | `Aria` flag for topology plans | `Aria: true` |
| `storeFacts.Ancestry` (authz_facts.go) | authz lineage climbs conversations | climbs through a log |
| `lineage_handlers.chainOf` | fork chains compared link by link | a log's chain is a conversation chain |
| `gc.Rows` / `gc.Stumps` | candidates for `figaro gc` | a row; `system.keep` protects the captains-log |
| `markReaped` (kill.go) | reaping marks only conversations | reaped like any aria |
| `carryBeforeDetach` (box_keys.go) | carries grants before a detach | carried |
| `headForkOfAConversation` | fork at head without waking | applies |
| `seedForkMeta`, `dressedAs` (topology_handlers.go) | outfit lineage on fork meta | a log has no outfit stump; empty |
| `studiesAFigaro` (sandbox_grant.go) | a study of a figaro's bound form is owner-only (#74) | owner-only, like a figaro. See Open 1 |
| `warmRows` (warm_listing.go) | listing warm-up | warmed |
| `list` (list_handlers.go) | rows for conversations vs anchors | a conversation row whose `kind` the sidecar sets to `log` |
| `worker_deliver`, `worker_say.speaksFor`, `worker_release` | pool workers are conversations | a log is never a pool worker; the switch admits it and nothing leases it |
| `form_apply` (unbound role forms only), `box_held.formsMintedBy`, `worker_mint`, `worker_handlers` | forms only | not a form: excluded |
| `hub.kind == form` (hub.go, hubs.go, arias_form.go) | a form node refuses turn methods | a log's hub is an aria hub: it wakes an agent |
| `formBind` (protocol.go) | binds only forms and stumps | a log binds from a form or the null root; a log is not a bind parent |
| `grants_moved`, `quota_mirror`, `runtime_names` (store open) | migrations over conversations and forms | migrated |
| `retain.RetainForms` | retention on unbound forms | not a form: excluded |
| `applySealedLifetimeWrite` (xwal_backend.go) | lifetime writes on conversations and forms | applies |
| `KindWord` (study.go) | wording of refusals | conversation wording |
| CLI `complete_sources` (`ariaCandidates`, `idCandidates`) | completion candidates by listing kind | `log` added beside `conversation` |
| CLI `manage`, `verbs`, `status`, `form_family` | null, outfit and form rows; everything else is an aria | the default (aria) branch |

## Context safety

What a log feeds a figaro's context is escaped like #143. `figaro show` of a
log disarms reserved tags twice: before rendering, the `<` of a reserved tag
in any spelling (`<`, `&lt;`, `&#60;`) becomes `‹`, which neither markdown nor
the highlighter decodes; after rendering, every printed line passes the
provider's neutraliser and a match that ANSI escapes split (glamour highlights
code blocks) is disarmed too, since the bash tool strips ANSI before a figaro
reads it. This holds in the rendered, `-j` and `-v`/`-l` views. Study deltas of
a log's form are reminder bodies and are already escaped. A log forked into
a figaro replays its entries as user prose, and every prose block, whoever
sent it, passes the neutraliser on every wire (`provider.Prose`), so an entry
from any principal reaches the model inert. That changes what every provider
is sent; `scripts/live/senderlive.sh` (real models, a few cents) is owed
before release. When lane laws-v077's
tool-output neutraliser lands it covers every CLI output a figaro reads,
`listen` through tmux included. Ingrain will arrive as the `ingrain` reminder
kind (proposal/reminder-kinds), whose bodies pass the same neutraliser.

## Measurements

On a copy of the live store (3.1 GB of arias), an isolated daemon of this
branch under `strace -f`, 200 sequential `figaro send --id <log> -r` (each
waits for `turn.done`): 35.8 ms per send end to end through the CLI, and 3
fsyncs per append: 1 on the log's IR segment and 2 on the inbox spool (the
accepted prompt, then its commit), the spool cost an aria's prompt already
pays. No form fsync per append. Afterwards `status -j` read `kind: log`,
`turn_count: 202`. Ledger: captains-log 2026/11/20/lane-aria-C.md.

## The index form (step 6), as this primitive supports it

Per revision 3: the index lives on the log's bound form as
`index.<handle>.summary`, `.blob`, `.head` (monotonic, the one value a figaro
studies) and `.refs.<n>` (one key per reference); `runtime.heat.<handle>` is
written by a daemon module from a walk log; `log.repo`, `log.ref`, `handles`
and `index.decide.threshold` are owner-held. Entries never live in a list key:
studies key on `.head` and read entries by range (`figaro.read` with `until`).
None of these keys is written by step 4, and none is registered here: step 6
registers each with its writer and shape when it writes it. Runtime writes
already refuse caller patches to `runtime.*`.

## Open

1. Studies (decided): a study of another figaro's bound form stays owner-only
   (#74), logs included, with no special case. Lane A step 1 opens it under
   the same read grant as `figaro.read` for every bound form at once. Until
   then `index.<handle>.head` studies are the owner's, enough for the import
   and the first tests.
2. On a log, the first entry seeds `mantra` when it is unset, as on any aria;
   and a log forked from a figaro mid-tool gets that tool closed at its first
   append, as any turn start does.
3. The tail of Gluck's 11-19 comment: open, as in the work order.

## Release note

- New: aria logs (`system.kind = "log"`, `figaro new --log`): an aria with no
  model whose sends are entries.
- `figaro.read` takes `until`, a forward ceiling; capability `read.until`.
- Listings carry `kind: "log"` for a log, and `turn_count` for every aria.
- IR input messages carry `accepted` keys; a restart no longer re-appends a
  prompt that reached the IR before its spool write.
- Break: every `form.bind` by a figaro charges its `max_children` and quota,
  births the child with quota 0, and draws it under the minter, as
  `figaro.create` does.
- Change: every prose block is neutralised on the wire, the owner's included:
  a literal `<system-reminder` or `<law` in a prompt reaches the model as
  `&lt;`. Ordinary angle brackets are untouched. Break: an aria whose history
  holds such a tag re-encodes that prompt once and loses its prompt cache
  once.
- Break: `system.kind` cannot change after birth, and no writer, the owner
  and the daemon included, may give a log a provider or model.

## Step 5 (not started)

Keel renders a log over `figaro.read` (pages of turns, each an entry) and
`figaro.form` (the index), read-only, deep-linked by `<log>:<turn>`; keel
URIs derived by the viewer. Coordinated with keel's owner through the King
first.
