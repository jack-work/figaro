# Steps

Generated from `gen/model.py`. Each step is one release, bottom-up, each net-negative in lines unless the row says otherwise (decision D11). Track S is the runtime, K the clients, X the sandbox; K and X run beside S.

| step | title | from | needs | lines | status |
|---|---|---|---|---|---|
| S0 | figwal carve | carve 1 | - | -1,263 (measured) | shipped 0.58.3 |
| S1 | delete one-shot migrations | design H (first half), audit 11, review 7 | - | about -1,000 | proposed |
| S2 | flatten the store contract | design A (first half), review 1, audit 10 | S1 | -600 to -900 | proposed |
| S3 | split the store by layer | design A (second half), carve 2; differs from review 2 (D1) | S2 | -300 to -500 (forwarders) | proposed |
| S4 | one runtime layer | design B, review 3 and 6, audit 9 and 12 | S3 | -700 to -900 | proposed |
| S5 | checkpointed reducible channel | design C, review 4-5 (first half), audit 1 outcome, 3, 8 | S4 | net negative only with audit 3 and 8 riding (D11) | proposed |
| S6 | owned primitives | design E | S4 | at risk: must pay with deletions (D11) | proposed |
| S7a | hierarchy: Prefix and Materialize | design D1-D2, audit 6 | S5 | -150 to -400 | proposed |
| S7b | hierarchy: fork state as one form | design D3, review 4 (second half), review 6 | S7a | large negative (five places, six clocks) | proposed |
| S7c | hierarchy: ForkPoints | design D4 | S7b | small negative | proposed |
| S7d | module manifest and registry | design D5-D6, carve 7 (first half) | S7c | negative if per-verb tables in angelus fold into registrations | proposed |
| S8 | run(drained inbox) -> ops | design F | S6 | positive unless the bash harness ships outside the main module (D11) | proposed |
| S9 | the agent owns provider logs | design G | S6, S8 | negative (two translation paths become one) | proposed |
| S10 | final deletions | design H (rest), review 8, audit 13 | S9 | negative | proposed |
| K1 | client binary | carve 4 | - | near zero; pays with the 23 allowlist edges | proposed |
| K2 | split cli | carve 4, terraform 1.1 | K1 | must pay with dead code in the 52k-line package | proposed |
| K3 | command sets per module | design D6 | K2, S7d | small | proposed |
| X1 | tools through api/sandbox | carve 5 | - | small negative | proposed |
| X2 | sandbox identity and leased grants | captains-log 11-09 (leases, lineage grants), #39, #57, #56 | X1, S7d | design first | design needed |

## S0: figwal carve

Five packages under figwal/, nested-fork engine removed, boundary rule 2 tightened.

- From: carve 1
- Needs: nothing
- Lines: -1,263 (measured)
- Breaks: import paths only.
- Gate: done

## S1: delete one-shot migrations

Delete figwal/xwal/flatten.go (571), internal/store/migrate.go (354), rename.go, NeedsFlatten, meta_heal.go if migration-only, and the generations they gate. A store older than layout 4 is refused with 'upgrade through 0.58.x first'. Drops the cli -> figwal/xwal allowlist edge.

- From: design H (first half), audit 11, review 7
- Needs: nothing
- Lines: about -1,000
- Breaks: a store below layout 4 no longer opens. Gate: schema.json on gluck, spain and cheroot reports layout 4.
- Gate: doctor schema on three nodes; full suite; flake check.

## S2: flatten the store contract

Delete store.Backend (40 methods, one implementation), the 38 local capability interfaces, the 64 type assertions and the no-capability branches (ErrNoTrunkCapability, topo.ErrNoPromote, topo.FromTopology, dem.Unsupported). Callers take the concrete type.

- From: design A (first half), review 1, audit 10
- Needs: S1
- Lines: -600 to -900
- Breaks: nothing at runtime. Every open lane that touches store rebases once.
- Gate: full suite x3 -race on store and angelus; T1 bench rows unchanged.

## S3: split the store by layer

Merge XwalStore into XwalBackend, then cut the result along the stack: runtime (logs, forms, residency), hierarchy (births, fork, promote, demote, arrange, normalize, ttl, reaped; internal/topo folds in), bindings (study, libretto, roles), host ops (meta, quarantine, memory, moves). Log[T] and Entry move to runtime so provider and decide import the runtime, never store (carve 2's rule as boundary rule 6). store stops importing turns, tokens and config.

- From: design A (second half), carve 2; differs from review 2 (D1)
- Needs: S2
- Lines: -300 to -500 (forwarders)
- Breaks: import paths only.
- Gate: boundary rule added; full suite; IR read benches (ReadPage, TailBudgeted).
- Issues: #74 (folds into), #70 (easier after)

## S4: one runtime layer

segment, disk and log move under figwal/internal; log folds into disk (-400); one Log[T] (treeLog's cache into xwalLog, -500). Kind strings become opaque to figwal; moves leave xwal for federation. The public surface is primitives (aria, form), group commit, and two declared seams: Own and Prefix. angelus and cli stop importing figwal/xwal.

- From: design B, review 3 and 6, audit 9 and 12
- Needs: S3
- Lines: -700 to -900
- Breaks: import paths. Boundary rule 2 is replaced by Go's internal/ rule.
- Gate: IR and form hot-path benches against the S3 release; crashtest.

## S5: checkpointed reducible channel

A checkpoint file per reducible channel, one live segment of at most N patches, an MVCC version pointer cached in figwal/tree, a background checkpoint writer off the append lock. Forms move onto it; store.Form becomes a view (no second fold); system.seqs leaves the folded state. Delete the segment header mode, HeaderAt, SegmentHeaderAt, OnSegmentOpen, reseedEmptyActiveHeader, reducibleFold, the root watermark, seedEmptyLog. One form log type (audit 8) and one apply/diff (audit 3) ride along.

- From: design C, review 4-5 (first half), audit 1 outcome, 3, 8
- Needs: S4
- Lines: net negative only with audit 3 and 8 riding (D11)
- Breaks: ON DISK: layout 5, with a one-shot converter deleted one release later. A rollback below this release needs the pre-upgrade backup. Retention must keep your 11-04 rule (D9).
- Gate: El Inspector reads the layout rule; crashtest; form write and rotation benches; a converter run on copies of gluck, spain and cheroot stores.
- Issues: #70 (easier after)

## S6: owned primitives

runtime.Own(owner, spec); a private id namespace the router refuses; Open of an owned id by anyone but the owner refused; the owner is the GC root. Provider logs, librettos and caches move onto it, deleting their special cases (translation dir handling, libretto ids, gc carve-outs). Runs in parallel with S5.

- From: design E
- Needs: S4
- Lines: at risk: must pay with deletions (D11)
- Breaks: show and other tools that read provider logs read through the owner or a published version (open question D16).
- Gate: authz tests: an owned id is Hidden from every caller; full suite.

## S7a: hierarchy: Prefix and Materialize

disk.Log's Parent and forkBase are replaced by an injected Prefix (Read, StateAt, Last). Materialize(below) replaces absorb-on-delete. Nested fork directories and hasSubdirs go: the per-append ReadDir (about 4 of 5 us per append) leaves the hot path.

- From: design D1-D2, audit 6
- Needs: S5
- Lines: -150 to -400
- Breaks: ON DISK: fork directories flattened (layout bump, converter). Fork reads through parents are hot: bench them.
- Gate: IR ReadPage, TailBudgeted, ForkWith benches; crashtest; El Inspector (layout).

## S7b: hierarchy: fork state as one form

Fork state becomes the hierarchy's own reducible form: child -> {parent, base per primitive} plus drawn edges. It replaces the .fork and .node markers, the index's lineage half, @topology, and the six clocks; the topo tree algorithm (moved in S3) runs over the form. adopt (#72) and role succession become sets on that form.

- From: design D3, review 4 (second half), review 6
- Needs: S7a
- Lines: large negative (five places, six clocks)
- Breaks: ON DISK: markers retired (converter). Client epoch TopologyRev changes meaning: the pager follows the form version.
- Gate: El Inspector; promote/demote/succeed e2e in tmux; topology benches (topo-perf rows).
- Issues: #72 (folds into), #71 (independent; touches), #28 (partly blocked)

## S7c: hierarchy: ForkPoints

Bindings implement ForkPoints(id, at) -> {primitive: index}, replacing the x-join's main-LT stamping on every channel. The WAL keeps only group commit.

- From: design D4
- Needs: S7b
- Lines: small negative
- Breaks: nothing visible; forks of old stores read the stamps once through the converter.
- Gate: fork at every turn of a recorded tape; byte-identical provider requests after fork.

## S7d: module manifest and registry

The store manifest names the modules that wrote it; a store written with hierarchy cannot open without it. Each module registers its RPC verbs, default grants and CLI command set; the API advertises modules. Build sets follow (a figarod without hierarchy or federation), one nix output per set.

- From: design D5-D6, carve 7 (first half)
- Needs: S7c
- Lines: negative if per-verb tables in angelus fold into registrations
- Breaks: packaging: new nix outputs. A client older than this release sees no advertisement and keeps today's verbs.
- Gate: El Inspector (grants per module); a no-hierarchy build passes the runtime and bindings suites.
- Issues: #68 (independent), #51 (independent; helps), #47 (folds into), #45 (independent)

## S8: run(drained inbox) -> ops

Bindings call run(drained inbox) and apply the returned ops (records, form patches) as one group commit. Tool calls run async and return through the inbox. The bash harness is the second run.

- From: design F
- Needs: S6
- Lines: positive unless the bash harness ships outside the main module (D11)
- Breaks: interrupt semantics (D16: commit what streamed, or nothing).
- Gate: tape replays byte-identical; steering and queued-send e2e.

## S9: the agent owns provider logs

CatchUp moves out of each provider (anthropic.go, openaichat.go, responses.go) into the agent step; providers are pure encoders. Delete store/translate_on_append.go (129) and provider/on_append.go (91). Recovery truncates derived rows past the fig IR tail.

- From: design G
- Needs: S6, S8
- Lines: negative (two translation paths become one)
- Breaks: translation format version bump only if output changes; retranslation must stay byte-identical (prompt cache).
- Gate: byte-identical requests over the tape corpus; cache-read rows in perf-baseline.
- Issues: #73 (independent; land before), #66 (independent; fits)

## S10: final deletions

The layout converters of S5, S7a and S7b; translations v1 directories; caches left without a reader; one cache budget (segment payloads, tree, form window, raw page).

- From: design H (rest), review 8, audit 13
- Needs: S9
- Lines: negative
- Breaks: a store must have passed the converter releases.
- Gate: doctor schema clean on gluck, spain, cheroot.

## K1: client binary

Cut the 23 allowlisted edges: key constants to api/form, a models.list RPC, doctor's offline open behind figarod, dressing preview over RPC. cmd/figaro becomes client-only and cmd/figarod the daemon. Self-spawn execs figarod and becomes unit-aware (#60).

- From: carve 4
- Needs: nothing
- Lines: near zero; pays with the 23 allowlist edges
- Breaks: figaro --angelus; the figarod symlink; install.sh, nix outputs, the home-manager module, vendorHash. sdk paths kept.
- Gate: El Inspector (spawn path, after the 11-05/11-06 spawn-env incidents); tmux run of both binaries.
- Issues: #60 (folds into)

## K2: split cli

internal/cli splits into cli (verbs), pager (transcript, pit, form view, render) and input (lineedit, keymap).

- From: carve 4, terraform 1.1
- Needs: K1
- Lines: must pay with dead code in the 52k-line package
- Breaks: nothing outside the binary.
- Gate: tmux suites for pager and composer; modal-input rules.

## K3: command sets per module

The client reads the advertised modules and loads one command set per module: without hierarchy, new, send, state; with it, fork, ls tree, promote, demote, kill -r, succeed.

- From: design D6
- Needs: K2, S7d
- Lines: small
- Breaks: help text and completion depend on the server.
- Gate: tmux against a no-hierarchy figarod.

## X1: tools through api/sandbox

internal/tool stops calling sandbox/local.NewHost and reaches every sandbox through api/sandbox. ExecRequest gains RunAs (#38). A figbox binary for the box side.

- From: carve 5
- Needs: nothing
- Lines: small negative
- Breaks: the box serve entry point.
- Gate: El Inspector (sandbox boundary); the #39 probe table as a regression test.
- Issues: #57 (blocked by), #39 (blocks), #38 (folds into)

## X2: sandbox identity and leased grants

A door per box that stamps a sandbox principal; leased boxes bound to a figaro's grant set; a child never gets more than its parent; a privileged reserved block in the bound form that the parent edits.

- From: captains-log 11-09 (leases, lineage grants), #39, #57, #56
- Needs: X1, S7d
- Lines: design first
- Breaks: to be designed.
- Gate: El Inspector; multi-model audit like E1 step 3.
- Issues: #57 (blocked by), #39 (blocks), #28 (partly blocked)
