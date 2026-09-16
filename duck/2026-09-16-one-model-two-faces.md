# 2026-09-16: one model behind ls and state, in two faces

**Status:** brainstorm. One line of it shipped tonight (`:ls` is uncapped in
the pager); the convergence itself is unbuilt. Scribe: aria `90ec6584`.
Companion to `2026-09-16-attendance-form.md`, written the same hour.

## What Gluck asked

> The ls should always be -a in transcript mode, no reason not to since its
> scrolling anyway. How structured is that output?

> Go find the recent figaro where we talked about putting topology in a form.
> I'm not sure where that left off. But it was a good idea because it would
> allow us to converge the implementations for the ls ui, the state ui, and
> both of their TUI uis, which would allow us to add more robust features and
> characteristics to both. The only two versions would be continuous and
> snapshot, the former for tui and the latter for stdout.

## How structured that output is today

Short answer: structured on the wire, text by the time anything sees it, and
the pager sees it last.

- `runList` (`internal/cli/manage.go:80`) asks the daemon for
  `[]rpc.FigaroInfo` through `acli.List` or `acli.ListGlobal`, then does the
  scoping, expiry drop and subtree filtering client-side, builds a `figtree`
  and renders `tree.Rows()` through `listColumns(width, global)`. The cap is
  applied to rendered rows.
- `-j/--json` prints that same `[]rpc.FigaroInfo` raw and is documented as
  taking no other flags. So a structured face exists, and it is an escape
  hatch rather than a model.
- In the pager, `:ls` is not an overlay verb. It goes through
  `runThroughRouter`, which calls `routeCaptured`, which points the package's
  `stdout` at a buffer, runs the shell verb, and hands the captured TEXT to
  `setTranscriptCmdOut` as panel lines. The TUI is scraping its own CLI.
- `state` is the counter-example, and the one that proves the point.
  `liveForm` (`internal/cli/command_router.go:136`) routes every form READ
  away from the router and into the pit, which is a live view fed by form
  deltas and the intrinsic mirrors (`internal/cli/intrinsic_mirror.go`). The
  comment there already states the principle: "A snapshot in a repainting
  window is just a live view that lies when the form changes."

So `state` already has both faces (continuous in the pit, snapshot at the
shell) and `ls` has one, drawn twice. That asymmetry is the whole of what
Gluck is proposing to erase.

Shipped tonight, separately: inside the pager `ls`/`list` gets `--all`
appended unless the reader typed `-a`, `-n` or `-j` (`pagerUncapped`). The
ten-row cap exists so a shell prompt is not buried; a scrolling panel has no
such problem.

## Where topology-in-a-form left off

It was aria **`93ce8b17`**, mantra "is topology reachable through a form
contract", on 2026-09-10 around 23:30, consulting aria **`4d24c0bb`**,
"CLI speaks both doors; federation stays behind the angelus". No plan
document came out of it and no code was written. It ends mid-question.

Gluck's framing that opened it:

> Im thinking that might work better as just a first class form that is
> lazily created and which reflects what is on disk. forks can be atomic
> patches to the topology form, as can reparents. Disk normalizations neednt
> acquire locks, they just make atomic patches to update the pointers for the
> relevant paths, and then tombstone them. A reaper should run periodically in
> the background to garbage collect files on disk with no record in the
> topology form. the topology form would be authoritative and the disk would
> just be a hash table basically of arias. different stored would get
> different concurrency domains.

The three layers it would collapse, as verified in source then: figwal marker
files (`.trunk` / `.from` / `.fork`) as ground truth, `xwal.Index` as a
derived in-memory cache with a version counter, and `store.topologySnapshot`
(`internal/store/xwal_store.go:948`) as a second derived layer keyed on two
clocks and rebuilt by `ListLight()` over every live trunk. Measured at the
time: topology and labels around 302 ms on a 515-aria store. Today's
`@topology` form is authoritative for nothing structural; it holds
presentation overrides only.

### What the two arias converged on

- **Form is authoritative storage and the watch surface; mutation stays
  verb-shaped.** Fork and reparent become atomic patches, but patches the
  single writer reduces, not patches a client sends. High fan-out plus
  whole-document CAS means two unrelated forks would 412 each other forever.
  Mark it system-managed so a remote caller structurally cannot corrupt a
  forest.
- **Structure only. Liveness must not live in the form.** Structure is id,
  kind, parent, trunk, branched-LT, name, tombstones. State, token counts,
  context usage, mantra and last_active are presence: a separate, queried,
  uncached channel. Otherwise the rev bumps on every turn of every aria, the
  cache validator is worthless, and the writer that serializes structural
  mutations also serializes metrics.
- **A store UUID at the form's root**, minted with the form, as the name for
  a concurrency domain and the qualifier that makes `(store, id)` an address.
  Per-row provenance only for imported arias.
- **Ids are never reused within a store.** Tombstones persist.

### Where the federation aria pushed back

- Study wants a read-only projection at the door, schema-versioned with
  golden vectors, not the organ itself. Cross-store study is new machinery
  (a local daemon holding a remote subscription and re-emitting), not a flag.
- Rev is an excellent ETag and a bad anti-loop token: revs are per-store
  counters and dedup needs peer identity, which does not exist yet.

### The price, unchanged

The reaper becomes the first component whose failure mode is data loss
propagated to every observer. Tombstone before unlink, grace window, never
delete on absence alone, a self-describing per-dir footer as a rebuild hint
and never as authority, an asymmetric startup reconcile, and a `doctor forest`
that can rebuild the form from footers.

Unresolved when it stopped: "normalizations need no locks" holds only in the
sense that no topology lock is held across disk I/O. The patches still
serialize on the one writer, which is cure B in `plans/store-locks.md`. Open
question was whether the actor loop lands first as its own refactor.

## Tonight's addition: the convergence

Gluck's new argument for the same design is not storage, it is the client.
One model, two faces:

| face | who | shape |
|---|---|---|
| continuous | the TUI | subscribe, apply deltas, repaint |
| snapshot | stdout | read once, render, exit |

If the forest is a form, then `ls` in the pager is `form show @topology` with
a tree renderer, exactly as `state` in the pager is already `form show <id>`
with the pit renderer. The panel stops being scraped text. `figaro ls --watch`
across a mesh is the same subscription with a different transport. Features
land once for both faces instead of twice for neither.

## Open questions

Scribe's, flagged as such, arising from putting the two threads side by side:

1. **The ls table is mostly presence.** Today's columns are outfit, ver, fork,
   age, msgs, ctx, cwd. Under the "structure only" cut, the topology form
   carries the tree and almost none of the columns. So a converged `ls` reads
   two sources: a continuous structural document plus a presence channel that
   is queried and uncached. Worth deciding whether the TUI face subscribes to
   structure and polls presence, or whether presence gets its own intrinsic.
2. **The cap moves.** With a continuous model the row cap is a view concern,
   not a query concern, and tonight's `pagerUncapped` becomes a property of
   the face rather than an argv rewrite.
3. **Two renderers or one?** `figtree` renders the shell table; the pit
   renders a form as a tree. If both faces read one model, one of those two is
   the survivor, and the choice decides how much the shell table can grow.
4. **Selection.** A continuous ls in the pager invites what the pit already
   has: a cursor, Enter, yank. That is the point at which `ls` in the TUI
   stops being output and becomes navigation, which lands it against the
   attendance ring in the companion note.
