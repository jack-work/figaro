# Layer 2: provider logs off the store

## For Gluck (phone page)

Not drafted yet. Brought after the layers above it are settled.

**Scope.** Providers and decide import internal/store in 16 production files for Log and Entry (62 references) plus TailAfter, VerifyOnce, TranslatorEncoder, ErrBelowForkBase, FigaroHash and the study and libretto keys. The pinch moves Log, Entry and MemLog to a leaf package and adds the rule that provider and decide never import store (plan step S3, carve step 2). Expected hidden variable: the catch-up path (provider/catchup.go) reaches into the store's translator log, which the plan only resolves in step S9.

## Ledger

- Status: listed 2026-11-10, not started.
- Rulings: none yet.
- Open questions: none yet.
- Hidden variables surfaced: none yet.
- Crossing work: chat-aria (design/chat-aria 664cae6d) step 2 adds kind `log`. Advised 2026-11-11: its own package from day one, wired concrete from *XwalBackend, no new Backend methods or capability interface (S2 deletes both), with a boundary row (imports no figaro, provider, decide, authz, angelus, topo).
- Rollbacks: none.
