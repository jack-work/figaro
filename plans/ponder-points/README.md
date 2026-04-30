# Ponder Points — placeholder

> **Status:** scaffold for future work. Not implemented; not specified in detail. This file exists so future readers of [`plans/aria-storage/log-unification.md`](../aria-storage/log-unification.md) can find the throughline and so the user has a place to drop notes as the design solidifies.

## What

"Ponder points" are designated breakpoints in an aria's log where the model is permitted (or expected) to perform deliberate reflection without the user waiting on it, and where the harness can checkpoint reasoning state for resumption, branching, or experimentation.

The general shape, sketched at this stage:

- **Reflection is asynchronous from the user.** A ponder point fires after a turn yields, runs the model on a prefix that ends at the ponder boundary, captures the resulting reasoning state, and stores it as a sidecar on the LogEntry that triggered it. The next user turn benefits from the precomputed reflection without paying its latency.
- **Reflection state is cacheable.** The ponder-point's wire-format projection follows the same baggage-and-causal-masking discipline as ordinary turns: a ponder point at LogEntry N reads the prefix 0..N-1, never N+1. This is the property that makes prompt caching pay off and that lets a single aria branch into multiple downstream variants without the variants invalidating each other's caches.
- **Pondering is provider-policy-driven.** Whether a given turn ponders, for how long, with what budget, against which model — these are policy knobs that live under `chalkboard.system.*` (or a sibling reserved namespace), evolved via the standard rehydrate path. Different providers may implement ponder support differently or not at all; the policy declares the desired behavior, the provider satisfies it however it can.

## Why this is here now

The aria-log-unification work sets up the structural prerequisites for ponder points without committing to the feature. Specifically:

1. **Causal masking** of handlers ensures a ponder-point handler can operate on a stable prefix without future-state contamination — same property that makes per-message handlers safe.
2. **Per-message handlers with baggage cache** mean re-projecting a partial conversation up to a ponder point and emitting synthetic continuations from there is cheap (cache-hit on everything before the ponder; render-and-stash for the ponder itself).
3. **Sidecar patches on messages** mean a ponder-point's state mutation can attach to the entry it occurs on without forcing a new IR variant. The IR already supports `LogEntry{Message, Patch}`; a ponder result is just another patch shape.
4. **Reserved `system.*` chalkboard keys** give us a place to store policy knobs (which turns ponder, what budget, etc.) without polluting the conversational IR.

These properties aren't useful only for ponder points — they pay off independently for cache stability and clean state evolution. Ponder points are the eventual reason the architecture earns its complexity.

## What's not yet decided

Almost everything operational. Open questions when this gets fleshed out:

- What triggers a ponder point? User-explicit (a CLI command), implicit (every Nth turn), policy-driven (when context has accumulated enough state to merit reflection)?
- What does the ponder-point handler do with its output? Append a synthetic assistant entry to the log? Attach a patch to the triggering entry's sidecar? Write to a separate "reasoning trace" channel?
- How is the ponder budget controlled — total tokens, wall-clock, or model "thinking" time?
- Does a ponder point fire automatically after each turn, or is it a separate command?
- How does the user inspect / debug ponder-point output? `figaro context --ponder`?
- Branching: if a ponder point produces a checkpoint and the user wants to "fork" the conversation from that checkpoint, what does the on-disk shape look like? (Adjacent aria? A child aria with a parent reference? Something else?)
- Provider variability: do all providers support a "thinking" budget? What's the fallback for those that don't?

These will be settled when the user is ready to elaborate. Until then, this file is a marker and the unification work is the prerequisite.

## Relationship to current work

- [`plans/cache-control/SYSTEM-REMINDERS.md`](../cache-control/SYSTEM-REMINDERS.md) — completed; established prefix-stability and `cache_control` wiring. Ponder points consume this property (cache hits across reflection runs).
- [`plans/cache-control/prompt-caching-limitations.md`](../cache-control/prompt-caching-limitations.md) — limits on the OAuth path that constrain when caching actually engages today. Ponder points only become economical once caching is paying off; on the OAuth path, the policy gating is upstream of any harness-level optimization.
- [`plans/aria-storage/log-unification.md`](../aria-storage/log-unification.md) — the structural prerequisites. Ponder points should not be specified or implemented before this lands.
