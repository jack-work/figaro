# Layer 2: provider logs off the store

## Ledger

Status: implemented on lane/history-log-carve. The final normal Go and flake gates passed on implementation commit 7b19cdb6. This continues the Nov 8 bottom-up carve and Nov 10 gradual separation; it is not a new architecture plan.

First landing: figwal/xwal owns the stored ancestry walk and contributing historical references. store no longer selects those ancestors. Full inspection retains turn annotations; history does not read .forkturn. Trunk/figaro IDs remain the tree-cache reference keys; physical node keys remain substrate details. Presentation overrides are unchanged.

Second landing: Entry, Log, MemLog and generic traversal/batch/hash helpers move from store to internal/arialog without compatibility aliases. The typed store adapter and runtime construction stay in store. The duplicated coordinate splitter in tree_log is deleted; materialized and peek reads now use figwal/tree.Split.

Dependency boundary: provider and decide production code import no internal/store. arialog imports only the standard library and figwal. The existing boundary ratchet enforces both without new allowlist entries.

Hidden variables resolved by explicit construction inputs:

- Provider construction previously received the whole Backend for translator opening and a ModelsForm type assertion. It now receives only an OpenTranslator function and a catalog snapshot; the host retains form opening and residency.
- An auto-route retirement check belonged to runtime policy, not transport selection. The host supplies its per-Send context check; providers do not parse store lifetime policy.
- Study key projection and the libretto-alive key belong to api/form, not the typed log package.

Disk state and behavior: no layout, schema, version or migration change. Per-channel LT and FigaroLT retain their distinct coordinates. Interior-fork pruning, full lineage inspection, hidden-row refusal and v5 derived repair retain the existing tests. No mutable ancestry cache was added.

Validation: focused affected store/provider/decide/daemon suites green; the existing lineage selection row moved to figwal. The immutable final build, vet, full Go tests, boundary ratchet and normal flake gate passed. No local Inspector or new worker. Net production Go source is -25 lines across the two landings, with moved files counted as moves rather than deletions; tests are +275 lines, mostly type/import adaptations.

Rollback: revert the relevant source commit. This carve writes no new disk state.
