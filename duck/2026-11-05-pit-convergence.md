# One form view for every pit

Base: v0.53.0 930ad862. Status: design, no code.

The pager already has the view Gluck likes: the form pit (`form_listen.go`,
`formView`), nested, live from deltas, editable through a replica since 0.53
(`c` edits, pending then settled, refusals taken back; read-only when the
daemon lacks `form.seq` or the form is an intrinsic). Three other surfaces show
form-shaped data their own way:

| Surface | Today | Data |
|---|---|---|
| models | pager pit `modelsView` (`models_view.go`): a table per provider (name, context, price columns), provider status line, `●` on the current choice; Enter picks a model for the bound aria | daemon catalog form `@models` |
| reaped | CLI only, `figaro reaped ls / restore / purge` (`reaped.go`): groups sorted by purge time, mantra, node count | daemon catalog form `@reaped` (`items.<root>.{mantra,nodes,at,purge_at}`) |
| topology | CLI `figaro ls` (`manage.go`, `figtree`): a tree with columns (mantra, handle, status, ctx, age) | the `list` RPC (live index), NOT the `@topology` form, which holds only parent/child edges |

## 1. What each shows that a plain form view does not, and where it goes

Everything splits three ways: data that belongs in the form, presentation
(how a node is drawn), and actions (verbs on a row).

- **Presentation hints**, one small descriptor per form, served by the daemon
  next to the snapshot (a `view` field on `FormResponse`, guarded by a
  capability `form.view`; an older daemon gets today's plain nested view):
  - `list: "<path>"`: draw the children of this path as rows instead of
    nesting (`items` for @reaped, `providers.*.models` for @models);
  - `columns: [{key, title, width?, align?}]`: which leaf keys of each row
    become cells (the existing `tabulate`/`figtree.Column` code renders them);
  - `sort: "<key>"` and `group: "<key>"`;
  - `mark: {path, equals}`: highlight the row matching a value (the current
    model).
  The hint is cosmetic. A client that ignores it still shows the full nested
  form, and the daemon never trusts it for anything.
- **Actions**: also in the descriptor, `actions: [{key, label, method, args}]`,
  naming an RPC the daemon already exposes and guards. The pit sends the
  method; the daemon authorizes it as it does today:
  - @models `Enter` -> pick: sets `system.provider`/`system.model` on the
    *bound aria* (owner-held keys, so the existing guard decides);
  - @reaped `r` -> restore (today `restoreReaped`: a set dropping
    `system.reaped*` on each node of the group) and `X` -> purge
    (`Kill{Now}`); `restore` becomes one daemon method so the client does not
    loop sets over nodes.
- **Data**: nothing new for @models or @reaped; they already carry every
  column.

**Topology is the exception.** `figaro ls` shows live status (turn running,
ctx, last access) that is not in `@topology` and should not be: writing it
there would add a form write to every turn and every status change. So
topology converges on the view, not on storage: the daemon serves the `list`
rows as a read-only *computed form* (the same snapshot/delta shape, rebuilt on
change, never persisted), with a `view` hint (tree by `parent`, the existing
columns). The client cannot tell it apart from a stored form, and it is
read-only by construction.

## 2. Read-only vs editable, from the daemon

The client must not guess. `FormResponse` gains `access`, computed for the
caller by the same code that guards writes:

- `form`: `read` or `write`. Catalogs (@models, @reaped) and computed forms
  are `read` (the daemon already refuses anything but `form` on a catalog,
  `catalogs.go:readCatalog`).
- `keys`: a deny list of key prefixes the caller cannot write on a writable
  form, each with its reason: system-managed keys (`form.WellKnownKeys`,
  `KeySystemManaged`), owner-held keys for a non-owner
  (`form.OwnerHeldKeys`), and for a box principal everything outside
  `boxWritable`.

The pit greys those rows and refuses `c` on them with the daemon's reason.
The daemon still enforces on every write (unchanged); `access` only stops the
client from offering an edit that would be refused. It is recomputed on
resync, so a role or policy change shows on the next delta gap or reopen.
Capability `form.access`; without it the pit behaves as 0.53 does (editable
per form, a refusal taken back).

## 3. Daemon-owned edits stay actions

A catalog is never written through `form.set`. Restore and purge on @reaped,
refresh on @models, and anything else that changes daemon-owned state, are
actions (section 1) that call a daemon method; the catalog changes as a
result and the pit sees it as a delta. Picking a model writes the *aria's*
form, not @models.

## 4. Migration, in order

1. `access` and `view` on `FormResponse` (daemon), the pit honouring both
   (client). No surface changes yet; the plain form view gains read-only
   rows.
2. @reaped in the pager: `:reaped` opens the form pit on @reaped with its hint
   and actions; `daemon method reaped.restore`. `figaro reaped ls` stays a
   printer over the same data.
3. @models: `modelsView` replaced by the form pit with the models hint; the
   `●` mark and Enter-to-pick move into `mark` and an action. Then delete
   `models_view.go`.
4. Topology: the computed form for `list`, then `:ls` in the pager on it.
   `figaro ls` keeps `figtree` for non-interactive output.
5. Remove the per-surface special cases from `pit.go`/`command_router.go`.

Each step ships alone; each keeps the old surface until its replacement is in
a release.

## 5. Risks

- **A view hint becoming a second UI language.** Keep it to list, columns,
  sort, group, mark, actions; anything richer stays bespoke.
- **Per-key access drift**: the deny list must come from the write guard's own
  functions, never a copy, or the client will offer edits the daemon refuses
  (harmless) or hide ones it allows (annoying). A test asserts that every
  prefix the guard refuses for a given principal appears in `access`.
- **Computed forms on a busy daemon**: the topology form rebuilds on every
  status change; it must be coalesced (one rebuild per tick) and only exist
  while someone watches it.
- **Actions are a privilege surface**: each names an existing guarded method,
  and the daemon authorizes it as if called directly. No action may carry a
  patch the client built. El Inspector reads steps 1 and 2.
- **Wire**: `view`, `access` and `reaped.restore` are additive, behind
  capabilities, so older CLIs and daemons keep today's behaviour.
