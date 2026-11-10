# Decisions for Gluck

Generated from `gen/model.py`. Where the design (figaro-design, #75) and the carve plan (backlog section 3, the 11-04 audit, the 11-09 captains-log) disagree, or a question is still open. Each has a recommendation; none is taken.

## D1: How to split XwalBackend

- One side: design A: by stack layer (runtime, hierarchy, bindings, agent, host ops)
- Other side: review cut 2: by concern (logs, forms, hierarchy, ops)
- Recommendation: By layer. The review predates the design and the layer split is the one the later steps consume.
- Step: S3

## D2: Is the aria log its own module?

- One side: design: aria and form are two primitive kinds inside one runtime layer
- Other side: carve map: arialog (layer 2) beside form (layer 2), separate modules
- Recommendation: One runtime layer. Keep carve 2's rule (provider and decide never import store) as a boundary rule.
- Step: S3

## D3: Where the form fold lives

- One side: design C: the runtime owns a reducible channel with checkpoints; store.Form becomes a view
- Other side: audit row 1: the Form writes its own watermark and xwal channels go log-only
- Recommendation: Design C. The row 1 lane stopped on 11-04 because forks, detach, moves and crash reseed all need state at an index, which is the reducer under another name.
- Step: S5

## D4: Where internal/actor sits

- One side: design: runtime
- Other side: carve: layer 0 beside api
- Recommendation: Runtime. Nothing in api needs it.
- Step: S4

## D5: What the actor runtime holds

- One side: design: topology and ttl to hierarchy, librettos to bindings, outfits to host, moves to federation
- Other side: carve 6: residency, hibernation, topology, ttl, outfits/stumps, librettos and moves stay together as one actor runtime
- Recommendation: The design's finer cut. Carve 6 then has nothing left of its own and is absorbed by S3, S4 and S7.
- Step: S3-S7

## D6: Order of the client and sandbox work

- One side: design: no client or sandbox step; clients only appear as command-set loaders
- Other side: carve: client binary (4) and sandbox (5) before the actor runtime (6)
- Recommendation: Parallel tracks K and X, independent of S1-S10; K1 can start any time. Only K3 and X2 wait on S7d.
- Step: K1, X1

## D7: When the module registry arrives

- One side: design D5-D6: manifest and advertising arrive with the hierarchy module
- Other side: carve 7: registry and build sets last
- Recommendation: With the hierarchy (S7d): it is the first optional module, and an optional module without a registry is not optional.
- Step: S7d

## D8: When migrations are deleted

- One side: design H: last
- Other side: carve and audit 11: as soon as schema.json on the three nodes says layout 4
- Recommendation: First (S1). It is pure deletion and shrinks what S4 and S5 have to move.
- Step: S1

## D9: Checkpoint retention vs fork-anywhere

- One side: design C: trim patches below the checkpoint; past retention N, readers diff two folded versions
- Other side: your 11-04 rule: fork any aria at any point, form patches included; gc keeps StateAt answerable for every version (checkpoints, not holes); F6 parked
- Recommendation: S5 moves the fold off the append path and keeps every patch (N unbounded). Trimming waits for a gc design that keeps StateAt at every forkable version.
- Step: S5

## D10: Three layout changes or one

- One side: one release per step: S5, S7a and S7b each bump the layout with a converter
- Other side: batch the three into one layout bump at S7b
- Recommendation: One bump per step, each converter deleted one release later (S10), so each step stays its own release. Batching would make S5 and S7a unshippable alone.
- Step: S5, S7a, S7b

## D11: Net-negative rule for additive phases

- One side: standing order 11-08: every step net-negative in lines
- Other side: design C, E and F add new types (checkpoint writer, Own, run -> ops, a bash harness)
- Recommendation: C pays with the header mode and audit rows 3 and 8; E pays with translator and libretto special cases; F is likely net-positive: allow it, or ship the bash harness as its own module.
- Step: S5, S6, S8

## D12: Federation's place

- One side: design open question 1: its own optional module, or 'replicate a primitive set' over runtime and ForkPoints
- Other side: carve: moves in the actor runtime; peer and troupe in the daemon
- Recommendation: Its own optional module over runtime and hierarchy; moves leave xwal at S4.
- Step: S4, S7d

## D13: Who owns the ttl reaper

- One side: design: hierarchy deletes subtrees
- Other side: runtime GC collects by root
- Recommendation: The design's proposal: the runtime collects by root and hierarchy declares the roots. #71 (retirement then ttl) is a bindings policy on top.
- Step: S6, S7b

## D14: Session primitives

- One side: captains-log 11-09: back/forward history, attendance and undo are 'primitive forms', single writer, persisted on a timer
- Other side: today: internal/primitive.Sessions in the host, outside the WAL
- Recommendation: A durability policy on the runtime's form primitive (WAL, or snapshot on a timer), owned by a per-session actor, so session state is studyable like any form.
- Step: S6

## D15: Authz per module and lineage permissions

- One side: design open question 4: each module registers its verbs with default grants
- Other side: captains-log 11-09: reserved privileged block, parent edits child, a child never exceeds its parent
- Recommendation: Registry first (S7d); lineage grants need hierarchy to answer ancestry, so authz takes hierarchy as an optional input (X2).
- Step: S7d, X2

## D16: Carried open questions

- One side: interrupt: commit what streamed or nothing; owned reads for show; group commit beyond one actor step
- Recommendation: Answer before S8 (interrupt), S6 (owned reads) and S8 (group commit).
- Step: S6, S8

## D17: Separate go.mod files

- One side: design: modules (registry, build sets)
- Other side: carve: go.mod only for modules with an outside consumer (api, sdk)
- Recommendation: Packages plus boundary rules; go.mod for api and sdk only, and figwal when a second consumer appears.
- Step: S7d
