# Phase B — figaro forking on xwal (the GRAND model)

Worktree `xwal-forking` (off `main`), figwal **v0.5.0**. Goal: formidable
conversation forking — everything is a fork of a null root, one tree.
Design settled with the user; this is the build spec. Lessons/frictions in
memory ([[project_figwal_forking]]).

## The tree (one fork tree, physically nested)

```
arias/                         <- the data dir IS the null root (xwal root, manifest here)
  null genesis: IR=[genesis tic], chalkboard=[root defaults], then SEALED (read-only)
  └─ loadout node  L@<verhash>  (fork of null; chalkboard += L's patch;
     │                           system.loadout_name=L, system.loadout_version=<verhash> [immutable])
     ├─ conversation  (fork of the loadout node; rich/empty prefix shared)
     │  └─ conversation' (fork of a conversation = branching; shared rich prefix)
     └─ conversation ...
```

- **null** = the arias directory itself; a closed root node. Not empty (figwal
  can't fork an empty log): seeded with ONE genesis IR tic + the root-level
  default chalkboard, then sealed forever.
- **loadout node** = fork(null) + apply the loadout's resolved patch. One node
  per (name, version). `system.loadout_name` + `system.loadout_version` are
  immutable chalkboard keys (set once; reducer/set refuses to change them).
- **conversation** = fork(loadout node). Inherits loadout defaults via the
  chalkboard **watermark** — no more bootPatch injection.
- **branching** a live conversation = fork(conversation), same op, rich prefix.

## Fork identity (settled, figwal-native)

Forking a node freezes it (read-only branch point, keeps its id, becomes a
navigable index node) and yields **new-id children**:
- interior fork at K: node freezes at [..K-1]; original continuation [K..] = one
  new-id child; the alternative = another new-id child.
- fork the present (tail): figaro spawns the continuation as an explicit N-ary
  sibling; two new-id empty leaves; node frozen.
- a frozen node persists as an index until all its children are deleted.
- **No caller-specified ids** anywhere — system mints all ids (drop CreateWithID
  + the create RPC id param). Fork mints child ids.

## Chalkboard = the reducible trinity member (transitions, not re-sends)

- Patches live in the **chalkboard channel** (reducible, reducer `jsonmerge`,
  initial = `{}`... actually inherited from null's root defaults via watermark).
- **Inline transitions** written to the model conversation are sourced from the
  channel's patch entries keyed to each IR LT (encoder joins IR LT -> chalkboard
  patches for that LT), rendered as `<system-reminder>` blocks ONCE, persisted in
  the cached history. NOT re-derived from a snapshot.
- `StateAt(chalkboard, now)` materializes ONLY the immutable `system.*` request
  structure (credo -> system prompt, tools, max_tokens) each turn (cached prefix).
- bare `figaro set` appends a patch to the channel keyed to the NEXT IR LT; no
  empty IR control-tic anymore.
- Forks inherit chalkboard state via the watermark; pre-fork transitions live in
  the shared cached prefix; the branch's system prompt materializes from its StateAt.

## Versioning + hashing

- loadout version = value-stable content hash of the resolved loadout patch
  (canonical JSON), using figwal's `segment.ValueHash` scheme (same as the
  `_hash` sidecar) — one hashing convention project-wide.
- loadout TOML change -> new hash -> new loadout node; existing conversations keep
  their node+version; versions coexist. ("never change a prefix.")

## Resolution index (at the null root / arias dir)

- `id -> branch path` (resolve any aria id to its nested branch).
- `(loadout_name, version) -> loadout node id` (find/reuse a loadout node).
- Registry/angelus resolves through this. Crash-safe writes (tmp+rename).

## Store layer (thin over xwal v0.5.0)

- `xwalLog[T]` implements `store.Log[T]` over one channel: `Lookup`->xwal.Lookup,
  `Append`-> AppendMain/Append with `Fingerprint` as meta, reads via ReadAt,
  `Clear`->xwal.Clear, `Close` no-op. Payload = JSON of T (fingerprint in meta).
- `XwalBackend` implements `store.Backend`: one `*xwal.XWAL` per node (cache+close
  unit); `Open(id)` resolves id->branch, returns ir view; `OpenTranslation` does
  AddChannel-if-new + view. Chalkboard gets a State view over StateAt + a
  patch-append keyed to next IR LT (NOT a Log[T]).

## Migration

Back up the existing backend dir to a timestamped sibling; start fresh. No
dual-read. (User approved.)

## Testing

- Go unit tests: xwalLog, XwalBackend, chalkboard channel, loadout
  materialization/versioning, fork identity + index resolution.
- End-to-end daemon via the tmux skill (`~/.config/figaro/skills/tmux.md`),
  isolated FIGARO_RUNTIME_DIR + FIGARO_STATE_DIR (never the live daemon),
  inherited config/hush/auth: create (=fork loadout) -> turn -> set -> fork ->
  verify shared prefix, divergent chalkboard+IR, transitions inline, loadout
  version preserved across a loadout edit.

## CLI fork rendering (user reqs, build at the fork/list step)
- `figaro ls`/`list` gains `--json` (machine view of the tree).
- Default view shows hierarchy. Only the ~10 most-recently-interacted arias +
  their full direct lineage (ancestors), truncated past a depth cap.
- The user wants a HORIZONTALLY-heavy layout: each aria a line, lineage shown as
  aligned ancestor columns (a "vector column" of ancestor ids/mantras) so depth is
  visible side-by-side and "this one is forked deeper" reads at a glance. Sketch:
  one row per aria; columns = ancestry levels (arias › loadout@ver › conv › fork…),
  leaf's mantra as the readable label, recency + depth markers. Brainstorm/optionally
  web-search a good compact-ancestry layout when building this.

## Build order
1. store: `xwalLog` + `XwalBackend` + chalkboard State view (+ unit tests, green).
2. null-root genesis + loadout-node materialization + version hash + index.
3. rewire create = fork loadout; drop bootPatch + caller ids; chalkboard channel
   wired into agent + encoder (transitions from channel) + `figaro set`.
4. `figaro fork` (CLI + angelus RPC + agent) = xwal.Fork at IR tail, mint child id.
5. tmux e2e; then migration/backup.
