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

## Battery FINALS (quiet, base2 x head@3ef8488, -count=6)
- READ PATHS: AriaReadPage/Before +2-2.6%; ReaderPage/Tail ±3%;
  ReaderForm ~0; ReaderContext -11% at 10k. Flat. The forms work costs
  the read path nothing.
- DormantList: +6784% REGRESSION at 300 arias — my LastTS wrapper did a
  stump SCAN per row (O(n²)/list). Fixed in 4665eef: trunk-by-map first.
  After: 262µs→395µs at 300 (+50%, +1 alloc/row) — the honest price of
  live recency; batch API noted as follow-up.
- Machine: load 0.00 both halves; head half carried brief compile blips
  from M5 writing (seconds, spread across 6 counts).

## M5 landed
- 4665eef (the O(n²) fix), d966eff (study/drop/cast). Suite + -race
  green; grammar surfaces real-binary verified; happy paths socket-e2e
  (sandbox has no provider — stated).
- STILL OWED: twelve-aria stress + form-storm variant; actor-loop cast
  bench; role-read addendum number.
