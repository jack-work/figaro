# Layer ledger: pinching figaro's layers off one at a time

El Cantero, 2026-11-10, on figaro v0.62.1 (29315dad). This branch is `design/layers` on keel. It holds the ledger only; the target architecture and its decisions live on `design/modular-runtime` (docs/design/modular-runtime/README.md), which this builds on and does not restate.

Each layer below gets one file: a short page for Gluck on top (what the pinch does, what it costs, the decisions it needs, each answerable in a line), then the ledger: status, his rulings with dates, open questions, hidden variables surfaced, rollbacks.

## Ranked: which layer to pinch off first

Measured on 0.62.1 with the plan's layer map: 154,661 non-test lines, 114 imports that point the wrong way. "Edges to cut" counts production imports a pinch must remove.

| # | layer | lines | edges to cut | why this rank |
|---|---|---:|---:|---|
| 1 | [tools + sandbox](01-sandbox.md) | 3,912 | 2 | Already imports only api/* outward. Two edges: `tool` aliases `sandbox/local.Host`, and `cli/box.go` hosts the box server. The bwrap deletion (c8c030c3) already shrank it. |
| 2 | [provider logs off the store](02-provider-store.md) | ~1,300 moved | 16 files | Providers and decide import the store for `Log`, `Entry` and a few helpers (78 production references). No runtime change; carve step 2 of the plan. |
| 3 | [hierarchy and its guard](03-hierarchy-guard.md) | 2,678 | in flight | Decided by Gluck on 11-10 (design/hierarchy-guard ab6d4ae1); the promote-guard lane builds it. Its rule, hierarchy imports no authz, is the first ratchet row. |
| 4 | [agent off host credentials](04-agent-credentials.md) | 19,799 (agent) | 13 files | Providers, decide and the agent step call `auth` directly (10 symbols). Inject a token source instead. |
| 5 | [federation off the host](05-federation.md) | 4,564 | 11 | peer imports authz, troupe imports apikey, config imports troupe, the CLI imports troupe 8 times. |
| 6 | [bindings off the host](06-bindings.md) | 4,074 | 8 | The figaro shell imports authz (principals), livelog/aria and outfit. |
| 7 | [client binary](07-client-binary.md) | 61,530 | 80 | The largest and the only one that breaks packaging (`figaro --angelus`, figarod, nix outputs). Last. |

The runtime's own steps (S1 to S10 on the plan: migrations, Backend, checkpoints) are internal reshaping, not boundary layers, and keep their place in the plan.

## How a layer is pinched

Every pinch is the same five moves, each its own commit, each green and revertable alone:

1. **Page and ruling.** Gluck answers the layer's page. Nothing lands before.
2. **Rule.** The layer's import rule is added to `scripts/check-boundary.sh` with today's violations in the allowlist. No code moves; the count can now only fall.
3. **Motion.** Pure moves: files and symbols change package, imports are rewritten, nothing else. Checked by `git diff -M` showing renames plus import lines only, and the same test list passing.
4. **Cuts.** One commit per seam (an interface introduced, a dependency injected), each removing its allowlist lines.
5. **Close.** The rule's allowlist is empty. The ledger records what the pinch proved and what it surfaced.

A hidden variable found mid-pinch stops the pinch where it is: the rule stays (it costs nothing), the cut that hit it is reverted, and the variable goes to Gluck as a one-line question on the layer page. Rollbacks are recorded in the layer's file, never squashed away.

## Static analysis: the recommendation

**Keep the import-graph ratchet as the spine, and make it data-driven.** `check-boundary.sh` already does the hard part: 5 rules, 23 allowlisted edges, a stale-allow failure, run under `go list` with no new dependency. Carve step 1 proved it (rule 2 tightened, canary red). The change: rules come from one layer map (package prefix to layer, layer to allowed layers), so pinching a layer is one data line plus allowlist lines, not a new shell block.

Weighed and set aside as the spine:

- **depguard / arch-go.** Allow and deny lists without a baseline; neither lets a violation count only fall, and both add a linter dependency to the nix check.
- **A custom go/analysis pass.** Gives file-level rules, which only matter inside god packages (store, angelus). Worth it if and when a layer has to be enforced before its files leave the package.
- **Dependency structure matrix.** A view, not a gate. The plan's pages already render one per layer.

Used per pinch, not in CI:

- **Call graph (x/tools VTA)** on the package being split, to list which symbols each outer layer calls. That list is the seam: it says what goes in the injected interface. First use: which `store` symbols the providers reach (layer 2), which `XwalBackend` methods each layer calls (plan step S3).
- **Mechanical moves.** `git mv` plus an import rewrite (`gofmt -r` for renamed identifiers, goimports for paths), scripted per pinch so the motion commit can be re-run on a newer base instead of rebased by hand.

## Worked example

Layer 3, the hierarchy and authorization split, is decided (keel design/hierarchy-guard ab6d4ae1, duck/2026-11-10-hierarchy-guard.md) and built by the promote-guard lane. Its boundary rule (the hierarchy package imports no authz, in scripts/check-boundary.sh) is the first ratchet row. It is the shape every later pinch copies: the inner layer exposes a plan and a hook; the outer layer implements the hook; the inner layer never imports the outer one; a canary proves the hook cannot be skipped.
