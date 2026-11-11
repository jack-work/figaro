# Layer 3: hierarchy and its guard (worked example)

## For Gluck

Already decided by you on 2026-11-10 (Telegram with King XII, routed to the Figaro Engineer). Design: keel `design/hierarchy-guard` ab6d4ae1, `duck/2026-11-10-hierarchy-guard.md`. Built by the promote-guard lane after the troupe re-sign and W20, on v0.63.0. No page needed; this file records how it goes.

**Shape.** The presentation tree is a pure hierarchy: every operation is a move plan (the nodes whose drawn parent changes, with their new parents). An injected `Check(principal, plan) error` runs inside apply, so no handler can skip it; orphan re-home runs as the system principal. The default guard requires `kill` on every node whose drawn position changes (El Inspector XIV). kill -r and the TTL sweep delete exactly the set they authorized. Fast follows: `adopt`, then a two-phase accept on the receiving parent.

**Ratchet row.** The hierarchy package imports no authz, enforced in `scripts/check-boundary.sh`. It is the first row of the layer rules.

## Ledger

- Status: designed; build waits on the re-sign and W20.
- Rulings:
  - 2026-11-10, Gluck: separate the hierarchy from authorization; constraints are hooks, and authz is injected through them. Wants receiving-parent acceptance (two-phase). "If we could structure it that way today as part of this change or in fast follows, that would be great."
  - 2026-11-10, El Inspector XIV: severity LOW today. Blocks any future grant that gives `set` without `kill` until the fix lands. Fix: authorize every node whose drawn position changes.
- Open questions: naming (King XII to Gluck, 11-10): a role holds permissions, a binding grants a role, `system.grants` is really a scope. Gluck has not ruled.
- Hidden variables surfaced:
  - Promote and demote were gated as `set` on the moved aria only; the other node in the swap was never checked. It held only because the owner has `*:*` locally.
  - The drawn tree is not cosmetic: the TTL sweep and `kill -r` follow it, so a presentation move changes what gets deleted.
- Crossing work: chat-aria logs mint top-level; where a log sits in the drawn tree decides what kill -r and the TTL sweep delete (its H4). Advised 2026-11-11: placement and `--under` go through the guard's move plan once it lands.
- Interface draft 1 (promote-guard, 3ed113c2, 2026-11-11): package internal/hierarchy, Plan/Move/Verdict, Guard.Check, Apply under one lock, Engine implemented by store, nil guard refuses. Asked back: drop api/rpc import (verbs are the guard's); state topo's fate (S3 folds it in); enforce the edge-writer rule by unexporting writers behind Engine rather than a call rule the import ratchet cannot see; no Guard construction in internal/figaro. chat-aria log placement goes through Op Place.
- Rollbacks: none.
