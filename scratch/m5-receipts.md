# M5 receipts (standby 718ade35)

NOTE: chartered home for these was a live `@m3-receipts` form, but the
INSTALLED figaro predates `fig form new` (the live daemon is pre-M2) —
receipts live here until the forms build deploys, then they migrate.

## Battery pass 1 (NOISY — prime built concurrently)
- base (400bec6/figwal v0.13): /tmp/bench-base.txt — store half noisy,
  angelus half quiet-ish. Demoted to shape only.
- head: /tmp/bench-head.txt at 3ef8488 (three free re-points rode the
  chain: ffcd1ee → a2cd665 → 3ef8488 before the head compile).
- FINALS = base2 (quiet rerun, chained) × head. Pending.

## M5 build log
- WatchFormDurable (store): eviction-proof watchers, re-armed on Form
  reopen. Red test written: watch → evict → write → sink lives; cancel
  → evict → write → silent.
- Agent study/drop/cast: system.studies durable; sink gates on
  membership (drop silences even while a cancelled sink outlives on a
  live Form); castOp serializes via inbox — the casting-calls comment
  stands at castOp; cross-call to the role writer per Form's contract;
  -O mints the role BORN cast (target-aria in the birth patch).
- Projection: render-only <system-reminder name="study:@id"> blocks on
  the next input message, hard cap 8, never msg.Patches. OPEN (noted in
  study.go, not improvised): replay invisibility, dormant catch-up,
  coalescing/budget policy.
- CLI: study/drop/cast registered; grammar = form last, -O occupies the
  form slot, kind validation names slot errors; cast auto-forks from
  the default form when no aria is available and spells out the
  partial when the casting then fails.
- e2e written (runs queued behind base2): cast points+studies (and is
  idempotent), born-cast mint, bound-target refusal, studied patch
  projects into the next turn, drop silences immediately.
