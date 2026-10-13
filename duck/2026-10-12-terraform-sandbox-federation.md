# 2026-10-12: terraform, the sandbox contract, and closing federation

**Status:** design review and proposal. Nothing built. Written from five
parallel read-only audits of `main` @ `e1a8f147` (0.38.2), the aria store
(`~/.local/state/figaro/arias`), the `gateway` worktree, and the duck notes
of 2026-09-16. Pointers are `file:line` in `main` unless marked otherwise.

## 0. What the pasted review got right, and what it missed

The review's five findings hold. Three are sharper than it said.

1. **Authz.** `policy()` returns `authz.AllowAll()` for anything but
   `[authz] policy = "default"` (`internal/angelus/protocol.go:141-152`), and
   "default" installs one rule, `NoSelfForkDuringTurn`, a deadlock guard.
   The authenticator trusts the `x-internal-figaro-id` params field when
   enabled (`internal/authz/authz.go:63-80`, `api/rpc/caller.go:218-234`).
   **Missed:** the guard wraps only the angelus method map. The per-aria
   sockets `figaros/<id>.sock` are served by `ariaHub.handlers()`
   (`internal/angelus/hub.go:233-243`) with no guard at all, and
   `rpc.SenderFrom` (`caller.go:240-244`) renders the asserted id into the
   model's context unconditionally. Any same-uid process can speak into any
   aria as any other aria, regardless of config. No peer credentials anywhere
   (no `SO_PEERCRED`, in figaro, jkrpc or hush).
2. **Exec.** `exec.Command("bash", "-c", req.Command)` as the daemon's uid
   (`internal/tool/exec_local.go:48`). The denylist is five daemon re-entry
   vars (`:334-340`), not secrets: provider keys in the spawning shell's env
   pass through. **Missed:** `read`/`write`/`edit` bypass the `Executor`
   entirely and touch `os` directly (`read.go:109`, `write.go:81`,
   `edit.go:112`), so even a perfect exec sandbox leaves the filesystem open.
   Also missed: `tool.Executor` (`exec.go:41-46`) is already the intended
   seam, with one implementation.
3. **Store.** Hand-rolled, yes, but the audit is kinder than the review:
   `go vet` is clean, the crash harness exists, and the core under
   `internal/store/{segment,disk,log,tree,xwal}` (~6.6k LOC) is already
   nearly figaro-free. The real problems are four layers where two would do
   (`disk → log → xwal → store`), **no versioned MVCC** (immutable-publish
   with bounded tails; `Form.state` is the only true atomic-snapshot
   structure, `internal/store/form.go:44-55`), one hidden store-wide
   serialization on cold open (`XwalStore.mu` held across `trunks.Head`,
   `xwal_store.go:386-390`, which waits on a mid-append lineage), five
   `Backend` methods with zero callers (`KeepStump`, `ForkAt`, `ApplyFormIf`,
   `ApplyFormEffect`, `SubscribeForm`), and the non-unix flock no-op
   (`xwal/flock_other.go`). One deterministic test failure on main:
   `TestOpeningAForkWithoutAResidentAncestorDecodesItsOwnCopy`
   (`internal/store/decode_duplication_test.go:122`).
4. **Monolith.** 112k non-test LOC, 130k test LOC. `internal/cli` alone is
   40k across 123 files and *starts the daemon in-process*
   (`internal/cli/angelus.go`). The CLI imports angelus, figaro, store,
   store/xwal, provider/*, tool, outfit; 39 cli files already go through
   `sdk`, so the thin path exists and is not enforced. `livelog/aria` fuses
   server and client halves. Two Anthropic providers are compiled in.
5. **Sprawl.** 146 worktrees, 228 branches; 72 worktrees are merged and
   clean, ~40 are 1000+ commits behind, 4 are current (`hush-revive`, `jev`,
   `meta-provider`, `quotestyle`). `figaro.test` (13 MB) is committed.
   `plans/` is 1.6 MB; `proposals/` holds opera-named agent reports.
   `agents.md` cites `ARCHITECTURE.md` (does not exist at root),
   `file_stream.go`/`mem_stream.go` (do not exist), and replace directives
   (`go.mod` has none).

What the review missed entirely: the federation work is not a gap, it is a
set of eight dated designs that were never reconciled (§2), and the
sandbox question is what currently gates the identity half of all of them.

## 1. The shape to converge on

Five subsystems, hard import boundaries, one multi-call static binary for
install (invariant 11 survives) with three `main`s:

```
figaro      thin client + TUI          depends on: api, sdk, jkrpc, term/render
figarod     angelus + figaro runtime   depends on: api, figwal, provider, sandbox
figbox      sandbox-side exec agent    depends on: api/sandbox, jkrpc        (tiny)
```

and two libraries:

```
api/        wire types, methods, protocol version   (already exists; becomes its own module)
figwal/     WAL + forest + snapshots                (extracted from internal/store/{segment,disk,log,tree,xwal})
```

The import rule, enforced in CI with `go list -deps`:
`cmd/figaro` must not reach `internal/{angelus,figaro,store,provider,tool,outfit}`.
`figwal` must not import anything under `github.com/jack-work/figaro`.
`internal/tool` must not import `os/exec` or touch `os` file APIs; it goes
through `api/sandbox`.

Why a multi-call binary rather than three files: the install story is one
`curl | sh`, and the point of the split is the import graph, not the number
of files on disk. `figarod` and `figbox` are symlinks the way `fig` is.

### 1.1 `internal/cli` (40k) becomes three packages

| package | content | LOC today |
|---|---|---|
| `internal/cli` | verbs, router, flags, firstrun, doctor, vault | ~12k |
| `internal/pager` | `transcript*.go` (13 files), `stream.go`, `livelog_bridge.go`, visual/selection | ~15k |
| `internal/input` | `lineedit.go`, `keymap.go`, `command.go` | ~4k |

`cli.go`'s one 1,652-line `buildRouter` splits per verb file. The daemon
entry (`cli/angelus.go`, `cli/provider.go`, `show_window.go`, `doctor*.go`
store paths) moves to `cmd/figarod` / `internal/angelus`.

### 1.2 `livelog/aria` splits

`aria.Server` (figaro/angelus side) and `aria.Client` (pager side) in one
package force the daemon to compile the client and the client to compile
the server. Split into `internal/livelog/serve` and `sdk/aria`.

## 2. Federation: the eight designs, and the decision

Found in trunk `61921be3` and its forks (`7f044820`, `4d24c0bb`,
`93ce8b17`), `bd0cf2bd`/`fc6afdfa`, trunk `7acf6682`, the `gateway`
worktree (branch `figaro`, 15 commits, 2026-09-29, unmerged), and
`duck/2026-09-16-*.md`.

| # | date | design | status |
|---|---|---|---|
| 1 | 08-09 | `plans/figaro-lab.md`: nspawn test fleet on spain; isolation = the four `FIGARO_*_DIR` knobs | proposal |
| 2 | 08-24 | CLI dials a remote origin behind Authelia | superseded same day |
| 3 | 08-25 | **peer mesh, "CLI stays dumb"**: CLI → local angelus → peers; control plane fanned out, data plane direct to owner; doorkey peer links, Authelia for browsers | `gateway/plans/http-gateway.md` rev 3; steps 0-4c built on the branch; spain deploy lost |
| 4 | 08-26 | `figaro new --node <peer>`; forks stay on parent's node; no ambient origin; peers symmetric in role, asymmetric in reachability | decided, not built |
| 5 | 09-10 | **"CLI speaks both doors"**: CLI can use a remote gateway natively like a browser, local angelus has the same contract, federation still behind the angelus | reverses rule 1 of #3; never reconciled into the plan |
| 6 | 09-10/16 | topology as a form; **store UUID at its root; ids never reused; `(store,id)` on the wire, bare locally**; presence out of the form; rev is an ETag not an anti-loop token | agreed in `duck/2026-09-16-one-model-two-faces.md` |
| 7 | 09-16 | **Orleans-style**: static hash ownership, client-chosen activation, single-writer live leaf, frozen prefix in an s3-like cold tier, lease epochs, flush at turn boundary | last owner-authored design; ended "iterate on this thought" |
| 8 | 09-16 | read-only federated mirror for a daemon-less TUI; session identity = pid + host | note |

Conflicts: dumb CLI (3) vs native CLI (5); client placement (4) vs hash
ownership (7); per-node store (3) vs cold tier (7); opaque ids (4) vs
`(store,id)` (6); `ls` fan-out (3, built) vs topology-form projection (6)
vs partitioned topology (7). Known bug: `peers.federate`
(`gateway/internal/angelus/peers.go:166`) forwards unmarked, so mutual
peers recurse; the agreed `no-federate` field never landed.

### 2.1 The resolution: one door

Designs 3 and 5 are not in conflict once the door is a single contract.
Define a **node door**: a jkrpc endpoint that carries

- a protocol version and capability list (replaces build-hash equality at
  `internal/cli/angelus_client.go:284-330`, which a peer can never satisfy),
- a node identity (the store UUID of #6),
- a **server-stamped principal**: aria, user, or node. The
  `x-internal-figaro-id` field stops being a credential and becomes
  attribution only; the server writes the principal from how the
  connection was authenticated, never from params. This is the fix for §0.1.

Three authenticators produce principals: `peercred` for the local unix
socket (needed the moment the daemon runs as its own uid), `doorkey` for
peer links, `oidc` (Authelia) for humans over the gateway. The broker
socket inside a sandbox is a fourth, trivial one: the socket exists only in
that sandbox, so the principal is that aria by construction (§3.3).

Then: the CLI speaks the node door whether it is `unix://` or `wss://`
(design 5), and the angelus speaks the same door to peers (design 3). "CLI
dumb" was a statement about *routing*, and that survives: the CLI never
federates, it talks to exactly one node and that node routes.

### 2.2 Identity, ownership, storage: decide now, defer the rest

- **Ids.** Opaque 128-bit random, never reused, bare short prefixes locally
  (git-style). The store UUID is the name of the concurrency domain and is
  recorded in aria meta as provenance; it is *not* part of the id. This
  satisfies both #4 and #6: the wire can carry `(store,id)` when it wants to
  say where, and the id is globally unique when it does not.
- **Ownership.** Single activation, single writer per live leaf, lease with
  epoch fencing (the half of #7 that is clearly right). **Directory by home
  node, not by hash.** With two or three nodes consistent hashing buys
  nothing and costs a partitioned topology form with cross-partition
  transactions. Keep directory separate from placement (the `fc6afdfa`
  split) so the hash function can be swapped in later.
- **Frozen prefix.** Forks across nodes read the parent's frozen prefix
  from the parent's node through the same fork-base delegation the WAL
  already does locally (`log/log.go:107-110`). No object store yet.
- **`ls` across nodes.** Fan-out with a hop-limit field on the request
  (fixes the loop), returning structure plus a presence channel per #6.
- **Placement.** `--node <peer>` explicit, default local, forks on the
  parent's node (#4). No ambient origin.
- **Reverse routing** (spain → NAT'd laptop) stays opt-in per #4: it is
  agent-triggered remote execution.

## 3. The sandbox contract

### 3.1 Principle

The owner's constraint from `7f044820`: "the container [must] distinctly
live outside the agent runtime, so we can keep figaro secrets isolated from
tool runtime." That rules out in-process `bwrap` wrapping as the only
mechanism: the boundary must be a process boundary that the daemon's
credentials do not cross.

### 3.2 `api/sandbox`

```go
type Provider interface { Open(ctx, Spec) (Sandbox, error) }

type Spec struct {
    Workspace string          // the one path the box sees as rw
    Grants    Grants          // zero value grants nothing
    Env       map[string]string // explicit; never inherited from the daemon
    Limits    Limits          // cpu, mem, wall, pids
    Ingress   Ingress         // how a nested figaro reaches a daemon (§3.3)
}

type Sandbox interface {
    Exec(ctx, ExecRequest) (Process, error) // streaming chunks, pty optional, exit/timeout/cancel
    Sessions() Sessions                      // background processes, opaque ids (not host pids)
    FS() FS                                  // Read, Write, Stat, MkdirAll, Rename, relative to Workspace
    Isolation() Isolation                    // what this provider actually enforces, for the form and the model
    Close() error
}
```

Every tool goes through `Sandbox`. `bash` → `Exec`; `process` → `Sessions`;
`read`/`write`/`edit` → `FS` (closing the bypass); `filelock.go` becomes an
FS concern; `cwdFromForm.isDir` (`internal/angelus/restore.go:177-190`)
asks the sandbox, not the daemon's `os`. Wiring is one line at
`internal/tool/registry.go:144` plus the two angelus sites
(`restore.go:139`, `topology_handlers.go:136`), selected per aria by a
form key `system.sandbox`.

Providers, in build order:

| provider | mechanism | isolates |
|---|---|---|
| `local` | today's `LocalExecutor` + `os` | nothing (declares so) |
| `uid` | `podman unshare` / `setpriv` to a dedicated uid, `bwrap` fs, `setfacl` perforation; measured at 33 ms in `7acf6682` | uid, fs, env, secrets |
| `box` | `figbox serve` spawned inside `uid`/nspawn/OCI, jkrpc over its stdio | adds a process boundary and the broker |
| `remote` | ssh/wss to a `figbox` or `figarod` on another host | adds the host |

`uid` is enough to protect secrets from a compromised model on one
machine. `box` is what makes the contract the same shape locally and
remotely.

### 3.3 Ingress: how a figaro inside a sandbox finds "its" daemon

Today a nested `figaro` resolves `$FIGARO_RUNTIME_DIR/angelus.sock` and, if
nothing answers, **spawns a second daemon** over whatever state dir it can
see (`internal/cli/angelus_client.go:107-130`). Before that it pings hush
and may spawn an embedded agent and prompt for a passphrase
(`cli/config.go:197-211`). Inside a sandbox with no mounted socket this
forks the universe. Two decisions:

1. **Auto-spawn leaves the CLI.** `figarod` is started by a unit or by
   `figaro daemon start` on a TTY. A CLI that finds no socket says so.
2. **`Spec.Ingress` is one of two modes, and they are the two routes the
   owner named:**

```
Ingress{Mode: broker}        the box gets ONE socket at $FIGARO_RUNTIME_DIR/angelus.sock,
                             served by figbox (or figarod for the uid provider), forwarding
                             to the HOST figarod over the node door with principal = this aria.
                             Default for local sandboxes.

Ingress{Mode: daemon, Peer}  the box (or remote host) runs its OWN figarod with its own store
                             and a peer link to the host node. The nested CLI dials it like
                             any local daemon; that daemon routes to the host when asked.
                             For remote hosts and for sandboxes that outlive one turn.
```

The nested CLI cannot tell the modes apart, which is the point: it dials
`angelus.sock` and speaks the node door. `FIGARO_ARIA` stays as the hint
for attendance; it is no longer what the server believes.

What the box needs regardless of mode: a `figaro` binary on `PATH`, the
bundled skills tree mounted read-only (they are unpacked on the daemon
host under `FIGARO_STATE_DIR`, `internal/outfit/bundled.go:83-89`, and the
model reads them by path), no hush socket, and a declared stance on
provider keys (never, in broker mode: the host makes the provider calls).

## 4. The store: converge on `figwal`

### 4.1 What to keep

The core is sound and already one-directional: `segment ← disk ← log ←
xwal`, `tree ← segment`. Keep: JSONL frames with `_hash`/`_idx` and
unparsed slicing (`segment/codec.go:139-166, 285-322`), segment rotation
and `.fork base=N` delegation (`disk/fork.go:17`), the pending-window
`atomic.Pointer` view (`log/log.go:60-75`), watermark headers for
reducible channels (`disk/log.go:538-567`), the flat layout with `.node`
lineage markers, the lineage cond-mutex, `tree.Cache` as the byte-budgeted
residency primitive, the crash harness.

### 4.2 Collapse to two packages

```
figwal/wal      segments + fsync + fork-base delegation + recovery   (today: segment + disk + log)
figwal/forest   nodes, trunks, lineage, index, flock, hot heads       (today: xwal + trunks + index)
figwal/tree     residency cache                                       (today: store/tree, unchanged)
figwal/snap     NEW: versioned state snapshots (§4.3)
```

`store/log` (656 LOC of pass-through) disappears. `xwal.Record`
(`{m,p,x,t,c}`) is the library's entry type; `store.Entry[T]` with
`FormChannelVersion`/`StudyVersions`/`FigaroHash` is a figaro tenant
concern and moves up.

### 4.3 `snap`: tree snapshots that never block the writer

What the owner asked for: an event-log WAL, a library for tree snapshots
of state, optimal point-in-time retrieval over those WALs, in-memory
snapshotting that does not block the write thread. The design:

- A reducible channel's state is a **persistent (path-copying) ordered
  tree** keyed by path. The single writer per lineage folds each WAL entry
  into a new root and publishes `{root, LT}` through an atomic pointer.
  Publishing is one pointer store. Readers grab the pointer and traverse;
  they take no lock, ever. Old roots stay valid as long as anything holds
  them, and **Go's GC is the version collector**: no refcounted
  `sharedView`/`hotHead` retire dance (`trunks.go:60-71, 797, 856`), which
  exists today only because views wrap mutable file handles.
- **In-memory MVCC** = a bounded ring of recent `{root, LT}` so `StateAt(LT)`
  for a recent LT is a pointer grab, not a fold.
- **Checkpoint** = serialize the root into the watermark segment at the
  channel's current LT, every N entries or bytes. With structural sharing,
  write only changed subtrees as content-addressed chunks; a checkpoint is
  proportional to what changed, not to state size.
- **Point in time** = nearest checkpoint `≤ LT`, replay `(ckpt, LT]` from
  the WAL. Fork base delegation gives a child the parent's checkpoints for
  free, which is what `headerfold_fork_test` pins today.
- **Forest-level snapshot** = immutable index version (`Index.version`,
  `xwal/index.go:23`) plus per-channel `{root, LT}`; `figaro.list` reads
  that and nothing else (invariant 15, unchanged).

`Form.state` (`store/form.go:44-55`) already is this shape with a flat map;
`snap` generalizes it, and the form, the topology form, librettos and
`AriaMeta` become tenants with a reducer each.

### 4.4 Fix list that falls out

- Kill the cold-open store lock: `openNode` must not hold `XwalStore.mu`
  across `trunks.Head`.
- `flock_other.go`: `LockFileEx` on Windows, or refuse to open with an
  explicit error. Silent no-op is the wrong default.
- Delete the five uncalled `Backend` methods; `Backend` (37 methods,
  `store.go:108`) shrinks to what the ~30 callers use.
- Move out of `store`: translation-on-append, libretto fold goroutines,
  outfits/stumps/TTL, `ir_door`, `meta_heal`, migrations. They are tenants.
- Fix `decode_duplication_test.go:122` or decide the meter is wrong.

## 5. Terraform order

Each phase leaves `main` releasable. Phases 1-2 are independent of 3-4 and
can run in parallel worktrees (two, not forty).

**Phase 0, hygiene (one PR, mechanical).** `git rm figaro.test scratch/
result`; move `plans/`, `proposals/`, `duck/*.md` older than 2026-09 and
`HANDOFF-trunks.md` to a `history` branch (keep the three September duck
notes and this one); prune the 72 merged worktrees and every branch 500+
behind after a `git tag archive/<name>` each; fix `agents.md` (no
`ARCHITECTURE.md`, no `file_stream.go`, no replace directives; promote
`duck/ARCHITECTURE.md` to root and trim it); fix the failing store test.

**Phase 1, the boundary.** Move the daemon entry out of `internal/cli`;
split `livelog/aria`; split `internal/cli` into cli/pager/input; make
`api/` its own module; add the `go list` import check to CI; three `main`s
in one multi-call binary. No behavior change; the protocol stays.

**Phase 2, the sandbox contract.** `api/sandbox`, `local` provider (today's
behavior, declared), close the fs bypass, remove auto-spawn, `uid`
provider, then `figbox` + broker ingress. The server-stamped principal and
`peercred` land here because the uid split needs them. This is the
"sandboxing first" the owner wrote on 09-30.

**Phase 3, `figwal`.** Collapse the layers, extract the module, add
`snap`, retire refcounted views, fix §4.4. Forward-only, bump the store
generation (`schema.go`), no shims, per the dev-mode convention.

**Phase 4, the node door and federation.** Protocol version and
capabilities; node identity from the store UUID; rebase the `gateway`
branch's tunnel, doorkey, `peer add/ls/rm`, `Node` field and nix module
onto the new boundary (do not merge it as-is); hop-limited `ls`;
`--node` placement; Ingress `daemon` mode. Reverse routing and the cold
tier stay out.

## 6. Open questions for the owner

1. Three `main`s in one binary, or truly separate files? The doc assumes
   multi-call.
2. Does `figbox` speak jkrpc over stdio, or does the broker socket live in
   `figarod` with `bwrap` mounting it in? Stdio keeps secrets out of the
   box entirely; the mount is less code.
3. Directory by home node now, hash later: agreed, or is the Orleans
   model the thing to build first?
4. `(store,id)` on the wire with opaque ids: agreed?
5. Is the Windows flock answer "refuse" or "implement"?

## 7. Execution: lanes, convergence, isolation, testing

### 7.1 Decompose by file ownership, not by phase

Agents collide on files, not on ideas; `HANDOFF.md` already records
reservation collisions and strict merge ordering as the failure mode. So a
lane is a disjoint set of path globs plus one named seam it builds against.
Nothing else is parallelizable safely.

**Wave 0, one agent, serial, lands before anything forks.** Seams and
hygiene:

- `api/sandbox` types (§3.2), `figwal` public API (§4.2 signatures, no
  bodies), node-door types (protocol version, `Principal`), all as compiling
  stubs with doc comments. Small files; every lane imports them.
- `RESERVATIONS.md` at the root of the `terraform` branch: lane → globs.
- The gate (§7.4) as `checks.*` in `flake.nix`, including the import
  boundary assertion. CI runs `nix flake check`.
- Delete the five dead `Backend` methods, `figaro.test`, `scratch/`,
  `result`; move `plans/`, `proposals/`, old duck notes to a `history`
  branch; fix `decode_duplication_test.go:122`; fix `agents.md` facts.
  Worktree/branch pruning is produced as a script and left for the owner.

**Wave 1, five lanes, disjoint globs:**

| lane | worktree | owns | seam it builds against |
|---|---|---|---|
| `boundary` | `../boundary` | `cmd/`, `internal/cli/{angelus,provider,doctor*,show_window}.go`, `internal/livelog/**`, `sdk/` | `api/` as a module |
| `sandbox` | `../sandbox` | `internal/tool/**`, `internal/sandbox/local/**`, the two angelus wiring lines | `api/sandbox` |
| `figwal` | `../figwal` | `internal/store/{segment,disk,log,xwal,tree}/**` → `figwal/{wal,forest,tree}` | `figwal` API stubs |
| `snap` | `../snap` | `figwal/snap/**` (greenfield) | `figwal` API stubs |
| `door` | `../door` | `internal/authz/**`, `api/rpc/**`, `internal/angelus/{hub,protocol}.go` | node-door types |

**Wave 2, after the named parent merges:**

| lane | parent | owns |
|---|---|---|
| `cli-split` | boundary | `internal/cli/transcript*`, `stream.go`, `livelog_bridge.go` → `internal/pager`; `lineedit`, `keymap`, `command` → `internal/input` |
| `sandbox-uid` | sandbox | `internal/sandbox/uid/**` |
| `figbox` | sandbox + door | `cmd/figbox`, `internal/sandbox/box/**`, broker ingress, auto-spawn removal in `internal/cli/angelus_client.go` |
| `tenants` | figwal + snap | `internal/store/*.go` (translations, libretto, outfit/TTL, meta, door) rebuilt as tenants; cold-open lock |
| `federation` | door | rebase of the `gateway` branch's tunnel, `peers.go`, `peer add/ls/rm`, nix module; hop-limited `ls`; `--node` |

**Wave 3:** one integrator pass for `ARCHITECTURE.md`, README, `agents.md`
hot spots, and the release.

Eleven lanes, never more than five live at once, plus one persistent
integrator. Not forty worktrees.

### 7.2 Convergence

- **Integration branch.** `terraform` off `main`. Lanes branch from
  `terraform`, merge back into `terraform` by PR. `main` takes `terraform`
  only at the end of a wave, when the gate is green and `nix build` ships a
  binary the owner has used for a day.
- **One integrator, serial.** It rebases the next PR in the fixed order,
  runs the gate, merges or sends it back. It never resolves a conflict
  itself; the lane agent does, in its own worktree, with the integrator's
  diff of what moved underneath it.
- **Merge order within a wave:** smallest and most central first
  (`door`, `sandbox`), big moves last (`boundary`, `figwal`). Every big move
  is a `git mv` commit with **no edits in the same commit**, so rebases on
  top of it are rename-aware and the diff review is "did the content
  change" (it must not).
- **Reservations are enforced, not advisory.** A PR touching a glob it
  does not own is rejected by the integrator before the gate runs. If two
  lanes need the same file, that file becomes a wave-0 seam.
- **Rebase after every merge.** Each merge into `terraform` fans out a
  "rebase onto `terraform` and re-run the gate" task to every live lane.
  This is cheap agent work and keeps the final merges trivial.
- **Test count is monotonic.** A move commit must keep `go test -list .`
  counts per package summed equal. Deleting a test requires naming its
  replacement in the PR.

### 7.3 Isolation

- **Treebear worktrees**, `~/dev/figaro-qua/<lane>`, created with
  `git worktree add -b <lane> ../<lane> terraform`. Never
  `.claude/worktrees`. One agent per worktree.
- **One tmux session per lane**, `tmux new -d -s <lane>`, three windows:
  shell, gate, daemon log. The agent drives tmux with `send-keys` /
  `capture-pane` for anything that paints, per `skills/tmux-testing.md`.
- **One dev shell per lane, and the root must be keyed by lane.**
  `flake.nix:270` derives `FIGARO_DEV_ROOT` from the *preset name*
  (`figaro-dev-clean`), so two lanes in `.#clean` would share one runtime
  dir, one socket, and one daemon of whichever build started first: the
  exact trap `tmux-testing.md` describes. Every lane enters with
  `FIGARO_DEV_ROOT=$XDG_RUNTIME_DIR/figaro-dev-<lane> nix develop .#clean`.
  Inside, `figaro` is that worktree's stamped build, the daemon is that
  lane's, the store is that lane's. The live daemon and
  `~/.local/state/figaro` are never touched.
- `.#share-config` only for lanes that must run a real turn (`sandbox`,
  `figbox`, `federation` smoke). Everything else is `.#clean`: fresh hush,
  no credentials, no provider.
- **`sandbox-uid` needs host facilities**: `podman`, `bwrap`, a second uid.
  Add them to the lane's shell, or run that lane in the nspawn container
  `plans/figaro-lab.md` describes on spain, where a throwaway uid is free.
- Five daemons at the 512 MB soft limit is fine on the dev box; it is not
  fine inside one small container. Size the container or stagger.

### 7.4 Testing: the gate, and what each lane must prove

**T0 gate, hermetic, every PR, as `nix flake check`:**
`go build`, `go vet`, `go test ./...`, tape replays, and `checks.boundary`:
a `go list -deps` assertion that `cmd/figaro` reaches none of
`internal/{angelus,figaro,store,provider,tool,outfit}`, `figwal` reaches
nothing under `github.com/jack-work/figaro`, and `internal/tool` imports
neither `os/exec` nor file `os` APIs. Nix makes the gate identical in every
worktree and on spain: same toolchain, same vendor hash, no local state.

**Conformance suites, written in wave 0 against the stubs, owned by the
seam, run by every implementation:**

- `api/sandbox/conformance`: exec streams in order; cancel kills the
  process group; a backgrounded session survives the aria rebuild; `FS`
  cannot escape `Workspace`; `Env` is exactly `Spec.Env`; `Isolation()`
  is truthful (the test probes what it claims). `local` passes it by
  declaring nothing; `uid` passes it by enforcing.
- `figwal/conformance`: the `crashtest` harness ported as-is; property
  tests for `snap`: `fold(wal[0..LT]) == StateAt(LT)` for random LT,
  a snapshot taken under concurrent appends never changes, checkpoint then
  replay equals no checkpoint, fork at LT sees exactly the parent's prefix.
- node door: the principal is never read from params; every hub method is
  guarded; a version mismatch is refused with the documented error.

**Lane-specific proofs:**

- `boundary`, `cli-split`: `FIGARO_TMUX_SMOKE=1 go test ./internal/cli
  -run TestSmoke` in the lane's tmux session with `.#share-config`. Tapes
  first (zero tokens), smoke second (real provider, a pass means
  something).
- `figwal`, `tenants`: `go test -bench` before and after through
  `benchstat` (in the dev shell). Invariant 14 is a complexity claim;
  a regression in B/op on the append or catch-up benchmarks fails the PR.
- `sandbox`, `figbox`: run `figaro ls` *inside* the box and assert it
  answered from the host daemon: exactly one `figarod` on the machine
  before and after, no second store under the box's state dir. Then read
  `~/.config/figaro/hush` from inside and expect `ENOENT`. Then exfiltrate
  `env` and expect no provider key.
- `federation`: two lanes' dev shells are two nodes on one machine. Peer
  them over tcp loopback. `ls` on A shows B's arias with B's node id;
  mutual peering terminates (hop limit); a `--node B` birth lands in B's
  store and attends from A. Later, the same script between the laptop and
  spain's nspawn container.

### 7.5 What drives the agents

Treebear worktrees are created by hand (`git worktree add`), one Claude
agent per lane with a brief of: this doc, the lane's globs, the seam
files, the gate command, the tmux session name, and "report a branch, not
a merge". The integrator is one more agent, long-lived, serial. The
alternative is the repo's own `skills/parallel-arias.md`: figaro arias as
the workers with the same briefs, which is the dogfooding option once the
`sandbox` lane has landed and the workers can be boxed.
