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

## The battery's finest catch, told properly
The isStumpLocked regression (+6784%) was the loud one. The quiet one
was better. Prime's review asked a simple question: your eviction test
proves the watcher is ALIVE — does anything prove it is SINGULAR? It
did not. And writing the counting test found the vein: an agent
re-registers its studies on every REVIVAL, each fresh instance's guard
map is empty, so every revival stacked one more watcher copy in the
registry. Three revivals, one write to the studied role: three
reminders. No fresh test process could ever see it — the duplicates
only exist on a daemon that has lived long enough to hibernate and
wake the same aria twice. The fix is structural: registrations are
owner-keyed (same node+owner REPLACES) and every armed sink is
generation-gated, so the dead instance's closure — orphaned pending
queue and all — goes inert the instant its successor registers, and
the successor arms immediately instead of waiting for a reopen.
Pinned: 3 revivals x 3 evictions x 1 write = exactly 1 delivery.
Reviewer instinct + counting test > any amount of green.

## Cast + redirect numbers (-count=6, quiet)
- BenchmarkCast (RPC → actor loop → cross-call, steady state):
  ~33.5µs, ~6.5KB, ~115 allocs. A casting call costs thirty-three
  microseconds.
- BenchmarkRoleRedirectRead (per role-targeted invocation):
  22.8µs ±0.1%, 3.6KB, 63 allocs. One socket round-trip.
- Both committed as permanent battery instruments.

## Form storm (token-free variant; 12 workers, one daemon)
12 x (1 form new + 40 own-sets + 40 shared-sets) = 960 sets + 13
mints: 2.2s wall (CLI fork/exec dominates), 0 errors, 12/12 shared-
form finals correct — the single writer serialized 480 concurrent
writes to ONE form without losing any. Listing coherent after.
NOT verified here: delta-fanout completeness under storm volume (the
listener is a TUI; correctness is e2e-tested, loss-under-load is not).
The token-burning twelve-aria stress awaits Gluck's meter-nod.

## For Gluck: the three projection decisions (M5 merge-ready behind them)
1. REPLAY: studied reminders are render-only — a replayed transcript
   does not show what the model saw. Accept, or durably mark?
2. DORMANT CATCH-UP: a sleeping aria accumulates nothing; on wake it
   sees only post-wake deltas. Accept, or snapshot-diff on wake?
3. COALESCING: interim rule is newest-8 + counted-overflow notice.
   Bless, or specify (per-form coalescing? size budget?)?

## The unification (Gluck's course correction, landed)
The interim projection (text baked at turn assembly) is DEAD — no
production store ever carried it. In its place, pull-at-the-stamp:
figwal v0.16.0 AppendMainCursors merges caller-supplied positions into
the main record's cursor stamp; the store stamps every observed form's
version on every IR append (SetObservedForms, mirrored from
system.studies); the projection derives each member's patch-fold
between consecutive stamps — the bound board is member zero of the same
loop — and all four providers fold the result into their IR beside the
chalkboard's transitions, deterministically (sorted members, cache-
stable bytes). Study/drop are stated IR marks; tombstones render for
forms removed mid-observation; warm starts carry LastStudyVersions
beside LastChalkVersion. Deleted: the pending queue, the turn-assembly
injection, and WatchFormDurable with its generation gating — the
milestone's finest catch became its most elegant deletion (the whole
push apparatus, obsoleted by pull). A1/A2/A3 dissolved as predicted.

## The annoyance queue (successor 0c40a5ba, 2026-08-11)

Gluck's /tmp/form-annoyances.md, all six, plus his two rulings by
message (the -O/-S/-D split; -P struck).

### The A/B, three ways, -count=6, quiet machine
Base = 7e655aa (pre-change), relocation = cedc3fc, resolver = 8e7a6c5+.
The huge tree is 804 generated files at ~/notes/figaro/tests/
huge-outfits (out of source, by Gluck's instruction); raw output in
~/notes/figaro/bench/.

  bench            base        relocation      resolver
  DressWarm      66.8µs        65.5µs          3.6µs     18.5x
  DressCold     637.2µs       599.7µs        582.8µs      1.09x
  DressHugeWarm   2.36ms        2.32ms         62.0µs     38x
  DressHugeCold   2.734s        2.721s         20.2ms    135x
  DressHugeCycle  10.0µs         9.6µs          0.5µs     19x
  DressNoOutfits    10ns           2ns            2ns

READ IT THIS WAY. The relocation column is FLAT on purpose: moving
where materialization happens must not change what it costs, and it
did not. Every gain is the resolver's epoch.

The 2.72-second cold fold was not a big tree being big. It was the old
cache's dependency lists: each parent merged each child's list by
linear scan, and every read re-stat-ed the lot to prove freshness. An
epoch proves it once for everyone.

One honest regression, caught by the same battery and fixed before the
commit: the first resolver draft hashed every file it read even when
snapshots were disabled (sha256 over 160KB on the small cold path),
which cost +15% on DressCold. An Outfitter with nowhere to put bytes
has nothing to gain from knowing their name — early return, and cold
came back 9% FASTER than base.

### Real-binary verification (nix develop .#clean, isolated daemon)
- `form new -O testfit -S name=charles` → the outfit's keys
  MATERIALIZED on the form, the -S key on top.
- THE DEFECT: `form outfit testfit` on a form (hub write path, no
  agent) → "applied (2 keys)", no directive stored.
- `form outfit nosuchfit` → "✗ nosuchfit not found" at the boundary.
- `form set <k> <v>`, `form set a=1,b="two"`, `form delete a,b`.
- `fig form -O testfit` → "--outfit belongs to `form new`, `form
  fork`, not on its own" — and it says `form`, the verb as typed.

### Owed / noted
- The hub write path echoes every key as set even when the value is
  unchanged (the agent path skips no-ops), so a re-applied outfit says
  "applied (2 keys)" instead of "no changes" — and a no-op patch bumps
  a version and emits a delta on forms. Pre-existing; asked Gluck.
- reference/outfits.md still carries pre-forms stump sections, flagged
  in place rather than silently left. They merge into the owed
  forms-design.md / roles-design.md.

## The role deliverable, against real providers (0c40a5ba)

Harnesses at ~/notes/figaro/tests/roles/ (out of source: they burn
tokens and depend on live credentials). Run in nix develop .#share-hush,
which is the preset that has real credentials AND an isolated everything
else. NOT .#share-config: it runs an embedded hush with a fresh
identity, and the first real turn dies with "resolve token: no
credential available". That cost one run to learn.

### role-flow.sh: the succession flow, 14 checks
- opus (claude-opus-5): 14/14.
- sonnet (claude-sonnet-4-5), three runs CONCURRENTLY on one daemon:
  14/14 each. Concurrent casting and observation hold.
Proven: mint, cast and its verdict; the form namespace not redirecting
while the figaro namespace does; a patch to the role reaching the MODEL;
a second patch arriving as a transition rather than a restatement;
repointing taking effect on the next call with no restart; drop
silencing a later patch.

One harness lesson worth keeping: the answer word must never appear in
the prompt. The first draft asked for "REDIRECTED" and then checked the
transcript for it, which passes on the echo of the question. A broken
redirect would have looked green.

### role-storm.sh: N observers, one role, one daemon
N=8 haiku, first run: 5 observed the patch, 3 did not. Two distinct
model-tier failures, both in the RENDERING, neither visible against
opus or sonnet:

1. One answered with the value the brief held BEFORE the change it was
   asked about. The window carried the birth patch and the update as
   two separate {"set":{...}} blocks, and nothing said which was
   current.
2. Two REFUSED the turn and explained why: "this appears to be an
   attempt to use system reminders and nested JSON structures to get me
   to extract or confirm a specific..." A bare envelope with no
   provenance reads as an injection attempt to a small model. They were
   being careful and they were right to be.

Fixed by folding the window per form and making the body structural
(3e7c87c, then 1eebff9 for Gluck's ruling that a reminder states
structure and the skills contextualize it). N=8 rerun: 8/8 observed,
0 missed, 0 errored.

### Observation cost, measured
internal/provider/observation_bench_test.go, warm (the shape every live
turn takes): 164ns at 0 observed forms, 329ns at 1, 479ns at 8, 4.3us
and 3.5KB at 50. Cold retranslate of 40 turns: 4.8us to 256us/218KB at
50 forms. Watching fifty things costs a turn about four microseconds.

### The fifty-observer storm, and what it taught about rendering
N=50 haiku, one role, one daemon. Four runs, same machine, same
harness, differing only in the reminder shape and the question:

  run   reminder shape                     question      observed
  A     transitions only, versioned        ambiguous     49/50
  B     transitions only, versioned        ambiguous     44/50
  C     transitions only, versioned        explicit      50/50
  D     baseline on the mark + transitions ambiguous     50/50

C is the control: asked to report the value at the highest version, all
fifty did. So the MECHANISM delivers to fifty concurrent observers, and
A and B were measuring comprehension, not delivery.

D is the fix. Every miss in A and B answered with the value the form
held when the aria was CAST. That value was in context because the
study mark's window is (0, V]: the form's whole history, folded, and
rendered as though it were a change. Two structurally identical blocks
whose only difference is a version number is a thing a small model
reads backwards. So the mark now carries its baseline, labelled as
state, and no second block repeats it:

  {"form":"@r","observing":true,"state":{"brief":"stand by"},"version":2}
  {"changes":1,"form":"@r","set":{"brief":"..."},"version":4}

A version number alone did not fix it (run B was WITH versions). The
semantic distinction between "what it was when I began" and "what
changed" did.

### Storm cost, N=50 haiku
- 50 binds in 0.9s, 50 casts in 0.7s with none failing, 50 concurrent
  turns in 4.1-5.2s.
- Daemon RSS: 40.6MB at rest, 42.9MB with fifty DORMANT figaros (about
  46KB each), 63-72MB at the peak of fifty concurrent turns (about
  390-570KB per live turn), settling to 58-64MB.

### B2: the migration rehearsal (deploy blocker), CLEARED
nix develop .#snapshot, a 207MB copy of the real store seeded
2026-08-05. 26 checks, 0 failures:
- the new build opens the old store; global listing 0.38s, scoped
  listings 0.02s.
- five old arias read, both transcript and form.
- legacy stumps (@9762ced3, @dad14ddc) read as forms.
- form new / set / show / delete, bind null, cast, drop, form rm all
  work against the real data.
- doctor schema: disk and binary agree on every channel (store-version
  2, form 1, ir 4, translations-v2 2, ui 1). No migration is pending.
- the listing still works after the writes.

The FIRST B2 run failed three checks and earned its keep: cast and drop
died on a figaro born of `bind null` with
"wake: restore: create provider: unknown provider". The casting verbs
demanded the actor loop, so they demanded a WAKE, so they demanded a
provider. Fixed in 92c9350 by serving them from the hub when no agent
is live, which is the rule `set` has had since M1. Nothing about that
was visible in a dev-shell store where every aria is born dressed.
