# State layer: running progress

Live notes for whoever holds the role `@980dc16c`. Update this, not chat.

## SESSION 4 AT A GLANCE (aria b2b0c543)

Phase 9's second half: **the copy is now the thing that renders**. Before
this, four sessions had built, refcounted, swept, migrated and instrumented a
libretto that no render path read — every studied form paid a durable fold
for state nothing consumed. It reads now.

**Shipped** (`e0fd5c34`)

| | |
|---|---|
| the cursor namespace | the IR stamps `libretto:<sourceid>` holding the LIBRETTO's version. Legacy `study:` stamps hold SOURCE versions, no longer match, and render nothing — the skip Gluck's ruling licensed, not a second rendering path |
| the accessor | `studyAccessors` reads each form's libretto. **No render path touches a source form.** Keys stay source ids, so the block still names what the user knows |
| the machinery stays hidden | `librettoView` strips `system.libretto.*` except `alive` |
| deletions | `StudyNotes`, its field, its render branch and its tombstone: **deleted**. A dead source is `system.libretto.alive` on a copy that outlives it, rendered as an ordinary key change |
| no new field | `IncrementalProjection` is untouched, per 6c2d7b9f's warning about that seam |

**Two things found by building it, both in the direction that loses data**

0. **THE ORPHANED READER — found live, after the unit suite was green.** The
   accessor read the copy as a NODE, which opens a second Form over the
   libretto's channel. It replays at open, so the first render is right; the
   fold appends through the other instance, so nothing after it ever arrives.
   On a real daemon the studied block rendered **once and then froze
   forever**, while the IR stamps went on advancing (4 → 5 → …) and every
   count stayed healthy. `doctor librettos` was silent, `doctor mem` said
   `open=1 observers=1`, and the whole unit suite passed. See §12.5d. Refused
   by construction now: `b.form()` rejects a libretto id and names the call
   that is correct. **What caught it was `renderlive.sh` asserting on the
   WIRE** — the bytes the model was actually sent.

1. **The libretto's bookkeeping would have reached the model.** `at` moves on
   every fold; `refs` moves whenever some OTHER aria studies the same form.
   Reading the copy means reading the document the copy lives in, and one
   aria's context would have carried another aria's refcount.
2. **Stripping in place would have edited history.** The patch a view is
   handed is the store's own published value, shared by every reader of that
   log. `withoutBookkeeping` copies only when it must; a test asserts the
   store's patch survives the render, because the per-LT cache would have
   made the damage permanent.

**A timing change worth knowing**: a stamp names where the COPY stood, and
the fold is asynchronous. A source write landing just before an IR append
renders in the NEXT block. Nothing is lost — consecutive stamps still bracket
every patch — but a test that patches and immediately appends must WAIT for
the fold. `TestObservedFormsStampIRAppends` does.

### The drift audit Gluck asked for (built vs. originally described)

Fanned out to a read-only aria against `durable-forms.md` whole, `wym.md` and
`answers-forms.md` (Gluck's own words), and session 1. Fourteen rows; the
honest summary is that **the refcount design is honored in full** — including
`wym.md`'s own reversal from a study-set MAP to a SET — and the gaps are
these four:

| # | what he said | what exists |
|---|---|---|
| 8 | "record the deletion and **stop listening**" (`wym.md:21`) | records the death, never unsubscribes (`libretto.go:349-364`). A dead source's form stays subscribed, so it is permanently un-evictable — one pinned instance per deleted-but-studied form |
| 9 | "if a form with the same id is created again, the system should handle it" (`wym.md:22`) | **never kept.** A reborn form's channel restarts at 1, and both the seed guard (`libretto.go:218`) and the event guard (`:309`) silently discard everything below the dead form's high-water mark. `alive` stays false. No test, no note |
| 3 | the two-participant write, approved **conditionally on seeing the code** (`answers-forms.md:12`) | built since `ac3314bc`; the review was never asked for. The retry count went 5 → 32 under contention that exists because casts left the actor loop |
| 10 | "scrap the form projection … whole-form only" (`answers-forms.md:1`) | code is whole-form; the DESIGN TEXT still carries `"paths"` in the libretto document and still lists the union projection as open `[q13]` in §17 |

Also unremarked anywhere: `requireStudyTarget` admits an **outfit** as a
study target (`figaro/study.go:79`), while §12 says derivations may subscribe
only to primary forms.

Row 6 of that audit was "figaro stamps the libretto's cursor — not built".
This session is that row.

### What the model is actually sent, now (`renderlive.sh`, on the wire)

Studying a form, patching it twice before a turn and once after, the request
figaro POSTs carries exactly this and nothing else:

```
study        {"form":"@id","observing":true,"state":{"brief":"the studied thing"}}
study:@id    {"changes":2,"set":{"sha":"8b12f128","status":"merged"}}
study:@id    {"changes":1,"set":{"phase":"ga"}}
```

The mark carries the baseline (the seeded copy), the two patches that landed
in one window are ONE block of two changes — the fold coalescing, visible —
and the patch after the first turn is its own block. No `system.libretto.at`,
no `refs`, no `@libretto::` stump name anywhere in the context. Seven checks,
all asserting on the wire dump rather than on what a model said about it.

**Three of the four faults that run exposed were in the SCRIPT, not the
product**: a `set` with the wrong argument shape whose error I had sent to
`/dev/null`, a glob matching no file, and a grep for `"ga"` against a body
where every quote is escaped. That last one reported the switch broken for a
whole cycle after it was fixed. **A check that cannot pass costs exactly what
a check that cannot fail costs**, and both look like evidence while they lie.

### The fleet, against a base run made the same hour

`ariastress.sh --arias 12 --study --study-patches 300`, twice: once on this
work, once on `eaeda9f5` in its own worktree. Same box, same minute, nothing
else running.

| | base `eaeda9f5` | **this work** |
|---|---|---|
| turns answered | 12/12 | **12/12** |
| history build | 4.95 s | **4.97 s** |
| control | 0.16 s | **0.16 s** |
| daemon PSS loaded | 50.5 M | **47.2 M** |
| goroutines | 81 | **81** |
| heap_alloc | 14.6 M | 16.8 M |
| heap_sys | 31.2 M | **27.2 M** |

**Read that heap_alloc row carefully rather than as a regression.** It is an
instantaneous sample whose value depends on where the GC happened to be;
`heap_sys` (what the process asked the OS for) is 4 MB LOWER and PSS is 3.3 MB
lower, which is the direction that matters and the number a user can feel.
Comparing to session 3's 10.5 M would have been the mistake: that is a
different build on a different day, and the control column exists so that a
comparison is made against something measured beside it.

Turn wall was 8.17 s against the base's 5.18 s, and **that is not a claim
about this code**: twelve real provider calls dominate it, and the control
(0.16 s both runs) says the box was not loaded. A number whose variance is
somebody else's API is not evidence in either direction.

## SESSION 3 AT A GLANCE (aria d604c755)

The figwal memory job in both halves, the layer nobody had reclaimed at all,
and phase 9 built and wired except its projection. Details are far below;
this is the map, and "WHAT IS ACTUALLY LEFT" at the very bottom is the queue.

**Shipped**

| | |
|---|---|
| figwal payload cache | opening a channel stopped copying every payload into RAM. Lazy per SEGMENT, budgeted (`segment_cache_mb`, default 32), LRU-evicted, with an idle sweep. **A listing on 515 arias: 116.5 MiB retained → 48.0, and it tracks the knob (4 MiB → 17.6)** |
| figwal lazy open | a sealed segment is a FILE, not an open handle: only the newest is opened, the rest on the read that lands in them. Open a 32-segment log: 5.42 ms → 0.19 ms |
| the arena | an idle daemon now hands free heap back (`debug.FreeOSMemory` behind a two-sweep latch). **PSS after a listing then idle: base 251 → 259 MB; after, 141 → 51 MB** |
| phase 9 | **the libretto is BUILT and wired**: derived form, whole-form fold, refcount, death record, reconciliation sweep with migration, `doctor librettos`, and study/drop/fork/kill/import as refcount participants. The study mark rides the inbox now, which fixes a defect that had bricked two arias. Only the projection half is left, and it is blocked on a ruling |
| instruments | `doctor mem` reports `segment-cache=X of Y loads=N` and `librettos open=N observers=M`; `daemon_day_test.go` measures what a store costs once every aria has been looked at; `doctor librettos` audits and repairs |
| the real store | phase 9 runs against a copy of it (715 rows): study, fork, drop, and a sweep over 711 boards in 0.89 s that MIGRATES the eleven studies made before librettos existed |

**The headline number Gluck asked for**: merely LOOKING at every board on the
real store — no decoding, no rendering — cost **297 MiB of heap and 419 MiB
reserved from the OS**, and now costs **68.5 and 127**.

**Four of my own claims died on their own measurements** and are left in
place as falsifications: that lazy opening would cut memory (48.7 → 48.6
MiB), that it would cut the 2.7 s first listing (unmoved), that it would cut
file descriptors (1227 → 1219), and that a +475% benchmark regression was
real (it was a one-time cost amortized over 100 iterations; the steady-state
figure is +18%).

**Seven faults found in my own new code**, every one by driving or measuring
it rather than by reading it: recency stamped a globally contended atomic on
every read (reads got SLOWER with more readers, 26 ns on one core and 47 on
sixteen); a lost eviction race stranded bytes nothing could reclaim; the
mirror copied its source's TOMBSTONE and so sealed itself; the idle sweep
EVICTED a source and orphaned the fold, leaving a silently stale copy;
opening a libretto needed its source, so a study of a deleted form could not
be dropped after a restart; the migration guard skipped exactly the stores
needing migration; and `doctor librettos` declined to repair the only case it
existed for. The last four were found by `studylive.sh` and `realstudy.sh` —
**ninety seconds of driving the real verbs, twice, against a green suite.**

**Also closed**: the cold-open fold, dead for a structural reason (a real
form is ONE segment, so there is no earlier watermark to fold from — it
measured 20-25x SLOWER); and phase 7, deferred with the argument written
down rather than left as a gap note.

## SESSION 2 AT A GLANCE (aria c1d55d02)

Phases 6 and 8 closed, phase 3's wire half landed, one lock-audit item done,
and the memory question answered. Everything below is detailed later in the
file; this is the map.

**Shipped**

| | |
|---|---|
| phase 6 | the delete path buries what it takes: a death record before any unlink, a refused delete buries nothing |
| phase 8 | the presentation hierarchy IS a form; `internal/trunk` and `trunks.json` deleted (458 lines), promote is one atomic patch |
| phase 3 (wire) | a set answers `applied` / `unchanged` / `queued` with a version; the CLI stopped claiming writes it never made |
| lock audit #2 | `cachedLog` publishes an immutable snapshot; contended reads 11.45 ns to 1.30 ns |
| cold reads | `PatchesBetween` below the window went O(offset) to O(range): 142 us to 3.05 us, flat with offset |
| memory | translations measured then bounded; `figwal loaded-heads` reported; the OUTFIT column stopped re-opening a form per row (209 to 0) |

**Found, not fixed** — `fig ls` retains ~95 MB on a 515-aria store, none of
it in any cache figaro owns: figwal copies every payload of a channel into
memory when a log opens. See "WHERE THE MEMORY IS". The fix is
segment-granular lazy loading in figwal, and two candidate figaro-side
mitigations are written up with their trade-offs because both change
something a user can feel and both want Gluck's ruling.

**Three of my own claims died on their own measurements**, and each is left
in place as a falsification rather than deleted: translations as the missing
memory (they are 8% of the IR), a "+13% regression" that ten samples showed
was noise, and a version-addressed cold open that measured +338% and was
reverted in full.

**Bugs found while doing something else**: a stale topology decided delete
sets (so `fig kill -r` unlinked a fork figaro never listed); `byFK` was an
unbounded index living inside a bounded window; `resident_ir_bytes` counted
one of the two caches an open aria holds.


## HAZARD: two arias, one worktree

A reviewer fork checked `pr16` out **in this worktree** while I was working
in it, then switched back. For a minute `git log` showed a HEAD that was not
mine and `git status` showed files I had not touched; a `git checkout --` I
ran in that window restored files from the wrong branch. Nothing was lost
(every commit is in the reflog and on the branch) but the recovery cost real
time at 3am, and the failure mode is silent.

**Rule**: a fork that shares a cwd shares the INDEX and the WORKING TREE. If
you are spawned to review, read, or bisect anything, do it in your own
worktree:

```
git worktree add -f /var/tmp/<name> <ref>
```

`/var/tmp/figbase` exists for exactly this and is what every before/after
benchmark in this file was measured against.

## Standing setup

- **Worktree/branch**: `/home/gluck/dev/figaro-qua/incant`, `feat/incantations`.
  Treated as the bona fide source; nothing goes to main first.
- **Role**: `@980dc16c` (`name=state-layer-worker`), `target-aria` points at
  the current worker. Move it on handoff.
- **Heartbeat**: `figaro-state-layer.timer` (user, 10 min) runs
  `/var/tmp/figstate/tick.sh`, which reads context usage and messages the
  ROLE. Escalates: 85% mint a successor, 95% hand off. Log at
  `/var/tmp/figstate/heartbeat.log`.
- **Design of record**: `plans/durable-forms.md` (what and why),
  `plans/state-layer-implementation.md` (how, with code),
  `plans/lock-audit.md` (the two fast-follows).
  Answers: `plans/answers-forms.md`, `plans/wym.md`.
- **Questions**: `~/notes/figaro/form-work/`.

## Rulings in force

- Librettos: **whole-form only**, no projection. One per studied FORM, named
  `@libretto::<formid>` (deterministic from the source id), shared,
  refcounted, does **not** fork. API-level derived forms keep projection.
- figwal's flusher is **gated, not removed**. figaro uses no-flush,
  fsync-before-publish, no exceptions, every channel including the IR.
- Pure reduce precedes the append; fsync precedes publish; a failed sync
  rejects.
- N cursors per IR record is fine.
- Study's two-participant write: ordering, not two-phase commit. Show the
  code before deciding.

## Why the parent aria broke (CORRECTED, read this version)

`anthropic: messages.946: tool_use ids were found without tool_result blocks`.

**My first diagnosis was wrong and the fix I queued would have made it
worse.** Aria 6c2d7b9f read the raw IR of the poisoned aria (`arias/ir/n714`)
and found the result is not missing at all. It is **displaced by one
record**:

```
127 input   tool_result  toolu_01ByJoaN…
128 output  tool_invoke  toolu_01Tqv3GS…   <- the call
129 input   content:null study:{began:true, form_id:"@3c00e173"}
130 input   tool_result  toolu_01Tqv3GS…   <- its result, one record too late
131 input   prose
```

Record 129 is a **study mark**: contentless, but it encodes to a user message
carrying a study system-reminder, and it sits between the `tool_use` and its
result. The provider requires the result in the NEXT message.

**Cause**: `appendStudyMark` writes an IR record from an RPC goroutine with no
regard for whether the drain loop is mid-turn with an open call. The trigger
was `fig cast` from inside a tool call, which is exactly what "create a role
as step one" asks for.

**Do NOT synthesize a result for every dangling id.** That was my queued fix
and it would put TWO tool_results behind one `tool_use`, which providers also
reject, and would tell the model a call failed that in fact succeeded.

**Being fixed by 6c2d7b9f**, on its own worktree off this branch: encoder-side
hold-back (a user record with no tool_result is emitted after the one that
resolves the open ids, which unbricks existing arias without editing
history); the source fix (no out-of-band IR record between a `tool_use` and
its results, covering study, drop, and whatever the libretto later stamps);
and a truthful synthesized result only where a fork genuinely cut a live
call. Its files: `internal/figaro/repair.go`, `internal/figaro/study.go`,
`internal/angelus/study_hub.go`, the four provider encoders,
`internal/form/incantation.go`.

**Boundary while that is in flight**: this line of work stays in
`internal/store`, `internal/actor`, `internal/trunk` and `internal/config`.
Note that phase 4 did touch `internal/figaro/form.go` and
`internal/figaro/agent.go` (`SetIntent`, `applyControlPatch`) and added
`internal/form/protect.go`; none of those are on 6c2d7b9f's list.

**The self-cast deadlock is the same bug from the other end.** `fig cast`
on your own aria from inside a turn hangs, because the cast rides the inbox
and the inbox is running the turn that issued it. The corruption above is
the same operation going AROUND the loop instead of through it. One hangs
because it needs the loop; one corrupts because it bypasses it. Phase 9,
which makes study an ordinary patch on a separate node, should fix both, and
should be checked against both.

**Workaround meanwhile** (SUPERSEDED by `30dcd6ba`, which removed the
deadlock: a cast no longer rides the inbox. Still the right move against a
daemon running an older binary, which is most of the ones on this box):
patch `target-aria` directly
(`figaro state set --id <role> target-aria <aria>`), which is a form write
served by the hub with no agent and no IR record.

## Devshell quirks

- `nix develop` ships its own Go. A benchmark run inside it showed a uniform
  +8 to +10% against a bare-shell run, including the do-nothing control.
  Compare like with like or do not compare.
- `nix develop` builds from the git tree: **untracked files are invisible**.
  `git add` before running anything in a devshell or you will debug a
  compile error that is not real.
- `FIGARO_ARIA`, `FIGARO_NO_BIND` and `FIGARO_PROMPT` are set inside an
  aria's own shell. `attend` refuses, binding is disabled, and any test
  needing attendance must `unset` all three (and run under a real pty for
  the interactive paths).
- `bc` is not installed. Use awk.
- `pgrep -f` matches the script's own command line. Use `pgrep -x` or filter
  `$$`.

## Log

### 2026-08-12, session 1 (aria ae6633c1)

- Role minted, heartbeat armed, notes started.
- Docs already committed on the branch: `a2364d42` (libretto per-form,
  figwal as a true WAL, lock audit), `a44c956e`, `b056f193`, `170a11c6`,
  `186926b4`.

**Next steps** are the phase list in
`plans/state-layer-implementation.md` Part I. Working order: phase 0
(figwal), phase 1 (actor), phase 2 (form on the actor). Those three are
worth having even if nothing else lands.

### 2026-08-12, session 1: phases 0 to 2 landed

| commit | what |
|---|---|
| figwal `4f9ce6a` | `SyncChannelThrough` (XWAL + Trunks), `NoBackgroundFlush` option, test. Pushed to origin/master. |
| `1be1afa2` | `internal/actor.Lazy`: spawn on submit, batch drain, linger, dormant. Exit race tested and fuzzed. |
| `1d9aa71b` | `store.Form` on the actor; group commit; sync before publish; IR appends sync; background flush gated off for figaro. |

figaro now pins figwal at `v0.16.2-0.20260813012153-4f9ce6a665f6`. **The
flake's vendorHash will need resetting** before any nix build: edit go.mod,
build, take the "got:" value. That is the documented one-step dance in
flake.nix and it has NOT been done yet.

**Two mutexes gone from `Form`**: the write lock and the sink list. The only
one left in form.go is `MemFormLog.mu`, the test double, which the audit
justifies.

#### Deviations from the plan

1. **Sinks emit BEFORE the answer.** The plan had the drainer answer waiters
   and then emit. A caller returning from `Apply` has always been able to
   assume the delta reached the sinks (they ran under the write lock), and
   moving the fanout off its goroutine would have withdrawn that silently.
   The race detector found it as a test race first.
2. **Completion is per submission, not a ticket watermark.** The plan (and my
   first implementation) released waiters when `done >= myTicket`. Tickets
   are handed out before the queue is entered, so a HIGH ticket can land in
   an EARLIER batch than a low one, and the watermark then frees a waiter
   whose result has not been written. `formWrite.done atomic.Bool` per write,
   with the broadcast tick only as the wakeup. Found by `-race -count=3`;
   `-count=1` passed three times in a row first. **Run the store package at
   `-count=3` under race or you will not see it.**
3. **`Command`/`Result`/intent/session/seq are NOT in yet.** Phase 2 kept
   `Apply`/`ApplyEffect` signatures so the blast radius stays inside the
   store. Phase 3 changes them.

#### The performance picture, honestly

Measured with `-benchtime 20x`, `TMPDIR=/var/tmp` (nvme; `/tmp` is tmpfs and
will hide every fsync you are trying to measure, which is a trap worth
remembering):

| writers | ns/op (whole round) | per patch |
|---|---|---|
| 1 | 17,428,254 | **17.4 ms** |
| 8 | 36,578,540 | 4.6 ms |
| 64 | 37,322,649 | 0.58 ms |
| 256 | 95,281,903 | 0.37 ms |

**Group commit works**: per-patch cost falls 47x from one writer to 256.

**The 17.4 ms figure above is a HARNESS ARTIFACT. Do not quote it.** The
benchmark spawns a goroutine and joins a WaitGroup per operation at
`-benchtime 20x`, which is far too few iterations to be trusted. Direct
measurement, same filesystem:

```
append (buffered)      5.8 µs
SyncThrough (fsync)    3.37 ms
Trunks.Head (borrow)   3.0 µs      <- the "double borrow" lead was wrong
ApplyForm end to end   3.16 ms
FormState (control)    41 ns
```

**So a solo form patch costs exactly one fsync, and nothing else measurable.**
The actor, the batch machinery and the handle borrow are all noise beside it.
Against roughly 5 µs before, the regression is ~600x on a single write, and
it is the price of the WAL being a WAL rather than a bug to find.

The floor is the filesystem: a raw `fsync` of 200 bytes on this box's
`/var/tmp` measures **3.13 ms** over 50 writes. Nothing in figaro can go
faster than that per durable write.

What can actually be done about it, in order:

1. **Batch, which already works**: per-patch cost falls 47x from one writer
   to 256, because one fsync covers the batch.
2. **Preallocation plus `fdatasync`** (durable-forms §3.3): an append changes
   the file size, so `fsync` and `fdatasync` cost the same today;
   `fallocate` at segment creation makes `fdatasync` the cheap one. This is
   the only lever that lowers the per-sync floor.
3. **Accept it where it is already invisible.** A turn writes ten to fifty IR
   messages, so 30 to 150 ms per turn against a provider round trip measured
   in seconds. `fig set` at 3 ms is interactive-invisible. The path that
   would hurt is a script doing hundreds of sequential writes.

**Not yet done and important**: no before/after on the live stack, no
`ariastress.sh` run, no twelve-aria recipe, no memory numbers. Gluck flagged
that figaro can allocate ~2 GB and wants savings hunted alongside the
regression.

#### Next steps, in order

1. Chase solo latency (lead 1 above first: one borrow instead of two).
2. Reset the flake vendorHash, then run the suite inside `nix develop`.
3. `ariastress.sh` before/after, PSS and swap, plus the twelve-aria recipe.
4. Phase 3: `Command`/`Result`, intent (`assert`/`ensure`), session+seq, the
   no-op acknowledgement.
5. Phase 4: schema validation in the writer.
6. Phase 5: `SubscribeFrom` as register-then-snapshot, lock-free.

#### Live validation on the WAL build (nix devshell, real provider)

```
20 sequential form patches   275 ms total (13.8 ms each)
a real turn                  2692 ms, answered
daemon PSS                   43.8 M, anon 17.9 M, swap 0
heap_alloc                   4.8 M
goroutines                   21
```

The 13.8 ms per `fig set` is mostly CLI process spawn: an earlier control
measured `figaro list -j`, which touches no board, at 12.16 ms. The
mandatory fsync is hiding inside the shell's startup cost at the CLI
granularity, which is the honest way to read it: **interactively the WAL
change is invisible.** A script that patches through one long-lived
connection would see the 3 ms.

Also done this session:
- `nix build` works: flake `vendorHash` reset for the figwal bump.
  `sha256-y0FdOfhVnIIOXAXsI9S/wg+9aXQbRne/K4sLGZZdzD4=`.
- Full suite green inside `nix develop .#default`.
- `ir_window_mb` defaults to 4 MiB instead of unbounded. The decoded IR is
  63 to 86 percent of a real aria's footprint, so this is the single
  largest memory lever available and it was switched off.
- `keepMu` retired for an `atomic.Pointer[string]`, per the lock audit: one
  string with a documented lock-order hazard, and a pointer swap has no
  order to invert.

#### Still unbounded, and the next memory lever

`formState.patches` grows for the life of the process: every patch a form
ever took, decoded, resident. This is the last unbounded retention in the
store and it is the next memory lever after the IR window.

**The design, including the trap.** Two halves:

1. **Serve below the window from the log.** `PatchesBetween(after, upTo]`
   checks whether the window still reaches `after`; if not, read that range
   back through `FormLog.RangePatches` and return a fresh slice. Correct,
   allocating, and only on the cold path (a retranslate of old history).
   The hot path keeps the zero-copy view.

2. **Trim, but COPY when you trim.** Here is the trap: `commit` appends to a
   SHARED backing array and each published state holds its own length. That
   is what makes the view safe, and it also means **re-slicing the front off
   does not release anything**: a header pointing into the middle of an
   array pins the whole array. Trimming has to copy the tail into a fresh
   array, and copying on every write is O(history) again, which is the
   regression this whole line of work removed.

   So: copy only when the excess crosses a slack allowance, exactly the
   pattern `cachedLog` already uses ("compaction is batched behind a slack
   allowance, 362 ns, zero extra allocations"). Read that code before
   writing this one.

Not started. `PatchesBetween`'s zero-copy contract and its test
(`TestFormPatchesBetweenIsAViewNotACopy`) are what will catch a mistake.

#### Fleet regression: 12 arias, one daemon, one studied form (300 patches)

Same harness (`scripts/ariastress.sh --study --study-patches 300`), same box,
against the numbers taken before the WAL change during the incantations work:

| | before WAL | after WAL |
|---|---|---|
| turns answered | 12/12 | 12/12 |
| history build (300 CLI patches) | 4.11 s | **4.98 s** |
| turn wall (12 concurrent) | 4.53 s | 5.49 s |
| control (12 x `ls -j`) | 0.17 s | 0.16 s |
| daemon PSS loaded | 56.8 M | 58.6 M |
| `Pss_anon` loaded | 30.6 M | 32.2 M |
| swap | 0 | 0 |
| goroutines | 93 | **80** |
| heap_alloc | 14.9 M | 16.0 M |

Read it as:

- **300 mandatory fsyncs cost 0.87 s**, or 2.9 ms each, which is exactly the
  measured filesystem floor. Nothing is being wasted; that is the price.
- **Turn wall is up 0.96 s across 12 concurrent turns**, most of which is the
  IR now syncing per message. Against a provider round trip measured in
  seconds it is visible but not dominant.
- **Goroutines are DOWN 14%** (93 to 80) with 12 live arias, which is the
  lazy actor: forms no longer park a goroutine each.
- Memory is flat to slightly up. The `ir_window_mb` default does not show
  here because these arias have almost no IR (72 rows); it will show on real
  arias, where the decoded IR is 63 to 86 percent of the footprint.
- The control is unchanged, which is what says the rest of the table is
  measuring the change rather than the afternoon.

#### Assert reaches the user

`fig state delete` / `fig unset` now refuse a key that is not there:

```
$ figaro state delete --id @0c479e41 nosuch
error: set: jsonrpc error -32000: remove "nosuch": no such key
```

Threaded as `SetRequest.Assert` (absent means the older forgiving rule, so
nothing else changes), through the hub's `ApplyFormEffectIntent` and the
agent's `SetIntent`.

**Known gap, deliberate**: `Agent.Set` answers BEFORE the loop runs, so on a
LIVE aria the refusal is enforced but only reaches the daemon log, not the
caller. Dormant nodes (hub-served) return it properly. The rule is applied
on both paths; only the reporting differs. Phase 3's synchronous command and
acknowledgement closes it, and until then this is the one place the two
halves of a write disagree about anything.

#### The WAL claim is now tested, not asserted

`internal/store/form_crash_test.go`: a child process patches a form and
prints every version the writer said landed; the parent SIGKILLs it at a
random moment, reopens the store, and checks every acknowledged version is
on disk. 130 to 240 acknowledged patches per attempt, all durable, four
attempts.

The shape is figwal's `crashtest` harness narrowed to one form. Note that it
re-enters the test binary through `FIGARO_FORM_CRASH_CHILD`, and the store
package already had a `TestMain`, so the child hook lives in
`xwal_bench_test.go`'s `TestMain` rather than in a second one.

Run it against real disk or it proves nothing: `TMPDIR=/var/tmp`.

### Where things stand

| phase | state |
|---|---|
| 0 figwal `SyncChannelThrough` + flush gate | **done**, released, pinned |
| 1 `internal/actor.Lazy` | **done**, fuzzed |
| 2 form on the actor, group commit, fsync | **done**, crash-tested |
| 3 command/event/ack | **partial**: intent (`assert`/`ensure`) is wired end to end; command, event, ack, session and seq are not |
| 4 schema validation | not started |
| 5 `SubscribeFrom` | **done**, and reachable through `Backend.SubscribeForm`; no consumer yet (the libretto is its customer, and `form listen` already does register-then-read on its own) |
| 6 tombstones and leases | **tombstone done** (`Form.Tombstone`, `system.tombstone`, sealing, idempotent, survives reopen, rides the ordinary subscription stream). leases are the subscriber set (`Reclaimable`); the delete path does not call either yet. |
| 7 retention policy | not started |
| 8 topology form | not started |
| 9 derived forms, libretto | not started |
| 10 API refactor | not started |

Also done outside the phase list: `ir_window_mb` bounded by default (the
single largest memory lever, previously off), `keepMu` retired, the flake
vendorHash reset, docs updated in `forms-design.md` and `reference/forms.md`.

### Handoff summary (read this first if you are the successor)

**State**: `feat/incantations`, ~50 commits ahead of main, everything green
(suite, `-race`, devshell, `nix build`, crash test, live turns). The branch
is the source of truth; nothing goes to main first.

**Done**: phases 0, 1, 2, 5 complete; phase 3 partial (intent only); phase 4
half (`CheckWritable` exists, not wired). Plus, outside the phase list: the
IR window default, the patch window, `soft_limit_mb`, `actor_linger_ms`,
`handle_idle_minutes`, `form_patch_window`, the sync instruments, mutex and
block profiling, and the removal of `Kick` and two mutexes.

**The three rules that must not be broken by later work**:

1. **Durable before visible, with no buffer.** Reduce purely, append, fsync,
   publish. A failed sync rejects; nothing is applied before the sync, so
   there is nothing to roll back.
2. **Batch durability, never semantics.** Each write is reduced against the
   state as of its own position, or `ifVersion` stops meaning anything.
3. **`PatchesBetween` is a VIEW.** Its safety rests on the published array
   being append-only and capped. Anything that compacts, rewrites or hands
   out an uncapped slice breaks it silently.
   `TestFormPatchesBetweenIsAViewNotACopy` is the guard.

**Two traps that cost me time and will cost you the same**:

- `-race -count=1` passed three times on a real race. Run the store package
  at `-count=3` under race.
- `/tmp` is tmpfs here, so every durability benchmark and the crash test are
  fiction on it. `TMPDIR=/var/tmp`.

### The next three things, in order

1. **Correction, and it retires a task.** I claimed several times that
   `fig form listen` had the snapshot-then-tail race. **It does not.**
   `internal/cli/form_listen.go` dials with the delta handler installed and
   only THEN refetches the snapshot, with the mirror's version check catching
   a delta older than the seed. Register-then-read, already, with the reason
   in the comment. `SubscribeFrom` remains right for derived forms and for
   anything that needs a durable cursor, but `form listen` needs nothing.
2. **`cachedLog.mu` to a published snapshot** (lock audit): 34 uses of one
   `RWMutex` on the hot read path, guarding `rows`, `trimmed` and `byFK`.
   They become one immutable struct behind an `atomic.Pointer`, the same
   pattern `formState` uses. Contended reads stop waiting on appends.
   Sizeable and mechanical; do it with the benchmark in hand.
3. **Phase 4, schema validation.** Beware the blast radius: `KeySystemManaged`
   keys (`aria_id`, `system.cwd`, `system.outfit_version`,
   `system.forked_from`) are written by the angelus during birth and would
   need the privileged path, so land the privileged entry point FIRST and
   only then start refusing.

#### Instruments, and a trap in the crash test

Landed the two missing instruments:

- `figaro.wal.sync.duration` and `figaro.wal.sync.batch`: how long an fsync
  took and how many patches it covered. **A batch distribution collapsing to
  1 is the alarm that group commit stopped working**, which is the only
  thing keeping a mandatory sync affordable.
- Mutex and block profiling are now ENABLED under `FIGARO_PPROF`
  (`SetMutexProfileFraction(5)`, `SetBlockProfileRate(10µs)`). They were
  served by `pprof.Index` and returned nothing, because nothing turned
  sampling on. For a daemon whose writers are serialization points, that was
  the missing profile.

**The crash test is now opt-in** (`FIGARO_CRASH_TEST=1`), because in a full
`go test ./...` it hung the suite for ten minutes. On tmpfs a "sync" costs
nothing, so the child spins fast enough to bury the parent in
acknowledgements. Run it deliberately, on real disk:

```
FIGARO_CRASH_TEST=1 TMPDIR=/var/tmp go test ./internal/store -run Acknowledged -v
```

### Succession

- **Successor minted**: `c1d55d02`, briefed, told to read the plans and wait.
  Its id is in `/var/tmp/figstate/.successor`.
- **The role is `@980dc16c`.** On handoff:
  `figaro state set --id @980dc16c target-aria c1d55d02`. The heartbeat
  reads the role's `target-aria` every ten minutes, so moving it moves the
  wakeups with it.
- **Do not use `fig cast` to move the role from inside an aria**: it rides
  the inbox, the inbox is running the turn that issued it, and it hangs
  until the timeout. Patch `target-aria` directly.

#### A real tool loop on the WAL build

The IR now syncs per message, and a tool loop is the path that appends most.
One aria on `sonn5`, three sequential bash calls then a reply, then a warm
second turn:

```
turn 1 (3 tool calls)   13.0 s wall, 8 messages, answered DONE
turn 2 (1 tool call)     3.4 s wall, answered OK
daemon                   PSS 34.1 M, anon 18.2 M, heap 7.2 M, 26 goroutines
resident IR              11 rows, 1390 bytes
```

Eight messages is eight fsyncs, ~24 ms, inside a 13 s turn. **The IR sync is
0.2% of a real turn.** That is the answer to whether mandatory durability is
affordable on the conversation path: it is, comfortably, and it was the last
open question about the WAL change.

#### Where the 2 GB comes from

Gluck's live daemon, measured this session:

```
heap_alloc   115 MB      heap_inuse  149 MB      heap_sys  416 MB
resident_ir   22 MB across 4 resident arias
goroutines   306         mem_limit  2147483648
```

**The 2 GB is a configured ceiling, not consumption.** `armMemoryLimit` set
`debug.SetMemoryLimit(2 GiB)` unconditionally and it was not configurable. A
high ceiling is a LICENCE as much as a limit: Go collects harder only as it
approaches, so `heap_sys` sits at 416 MB while only 149 MB is in use and the
runtime has no reason to give the rest back.

Now `[memory] soft_limit_mb`, default 2048, 0 for none, `GOMEMLIMIT` still
winning. Lowering it is the one-knob answer to "figaro is holding too much",
at the cost of more GC cycles.

**Not investigated: 306 goroutines with 3 live arias.** That is a lot, and
the lazy actor only removed the per-form ones. Worth a `/debug/pprof/goroutine`
against the live daemon (now that profiling is armed) before assuming it is
fine.

#### Config, read back at the enforcement point

Validated live in a devshell, which is the test Gluck asked for: a
`config.toml` supplying the values, read through the loader, checked where
it is enforced rather than where it is parsed.

```toml
[memory]
soft_limit_mb   = 512
ir_window_mb    = 2
actor_linger_ms = 250
```

```
$ figaro doctor mem -j | jq -c '{mem_limit_bytes}'
{"mem_limit_bytes":536870912}      # 512 MiB, as configured
```

`[memory]` now carries `dormant_after_minutes` (existing), `ir_window`,
`ir_window_mb`, `soft_limit_mb` and `actor_linger_ms`. Still unwired from
config: figwal's `IdleUnload` (the second of the three idle clocks) and the
subscriber lease TTL (phase 6, not built).

#### Final measurements for this session

Read path, unchanged by everything above (this is the zero-copy view from
the earlier perf work, still zero-copy):

```
FormDeltaPerSend100/1000/10000    45 / 48 / 53 ns/op, 0 allocs
FormWholePerSend100/1000/10000    49 / 55 / 57 ns/op, 0 allocs
```

Independence, 16 forms patched concurrently on real disk:

```
FormApplyManyForms   5.9 ms for 16 forms
```

Sixteen fsyncs at ~3 ms each finishing in 5.9 ms of wall clock is the proof
that the domains stay independent: one actor per form, one lock per form,
nothing shared but the store.

#### Size of the change

```
non-test Go      1784 +   179 -
tests            2037 +    56 -
plans and docs   2974 +    35 -
```

The green is mostly tests and design records. Deletions that matter:
`Form`'s write mutex and sink list, `Kick` and its six call sites,
`keepMu`, and the buffered-durability story.

#### Phase 4, first half only

`internal/form/protect.go`: `CheckWritable(patch, privileged)` refuses an
unprivileged write to a `KeySystemManaged` key. The mode has been in
`WellKnownKeys` since it was written and had never been enforced.

**It is NOT wired into the writer yet, on purpose.** The keys it protects
(`system.cwd`, `system.outfit_version`, `system.forked_from`, `model`,
`root`, `token_budget`, `truncation`) are written by the angelus during
birth and by the harness per turn, and every one of those call sites needs
the privileged path before anything starts refusing. Wiring it without that
bricks aria creation.

The order for whoever picks this up:

1. Add the privileged entry point to `store.Form` (an unexported field on
   the write, set only by in-process callers; no JSON tag anywhere near it).
2. Find every harness write of a system-managed key and route it through
   that entry point. `runtimeFillins`, `birthPatch`, `childBirthPatch`,
   `forkDress` and the per-turn ephemeral keys are the ones I know of.
3. THEN call `CheckWritable` from `reduceOne`, before the reduce.
4. Only then consider shape validation, which wants a per-key validator and
   the provider-keyed system schema, and which is a separate argument.

#### Validation gate, end of session 1

All green, on commit `HEAD` of `feat/incantations`:

```
go build ./... && go vet ./...            clean
go test ./... -count=1                    clean, bare shell and nix develop
-race on store/figaro/angelus/actor/form  clean
nix build .#default                       builds
FIGARO_CRASH_TEST=1 crash test            acknowledged patches all durable
live: mint, patch, real turn, tool loop    all answered
```

#### The last unbounded retention is bounded

`formState.patches` now has a window (`[memory] form_patch_window`, default
2048, 0 retains everything). Two halves, as designed:

- **Below the window, read from the log.** `PatchesBetween` checks whether
  the resident array reaches `after`; if not it walks `RangePatches` and
  returns a fresh slice. Cold path only: a retranslate of old history. The
  hot path is still the zero-copy view, still 45 to 57 ns and zero allocs.
- **Trim by COPYING, across a slack allowance.** Re-slicing releases nothing
  because the array is shared by construction, so the tail is copied into a
  fresh array once the excess passes 256. Copying per write would be the
  O(history) cost this whole line of work removed.

The log walk is O(history) per cold call, because `FormLog` has no bounded
range read. Acceptable now (it happens on retranslate, not per Send) and the
obvious next step if it shows up: figwal already has the segment index and
`RecordsBetween` would be a thin wrapper.

**Workflow note, from Gluck:** long commands were held in the foreground and
blocked the actor loop, so heartbeats queued behind them. `/var/tmp/figstate/job
<name> <cmd>` runs work as a transient user service; `/var/tmp/figstate/jobs.sh`
lists status. Use it for anything over a few seconds.

**A bug the benchmark caught, in the window itself.** The first draft decided
"is this range below the window" by comparing against `patches[0].Version`.
That is wrong: a no-op patch appends no record, so a form whose early
records changed nothing legitimately starts above version 1, and reading
that gap as a trim sent EVERY cold read to the log. The benchmark showed
`FormWholePerSend100` at 141 µs and 1245 allocs where it should be tens of
nanoseconds and zero.

`formState.trimmed` records the highest version actually dropped, and only
that sends a read to disk. After the fix:

```
FormWholePerSend100     55.8 ns   0 allocs   (resident)
FormWholePerSend1000    41.8 ns   0 allocs   (resident)
FormWholePerSend10000   12.9 ms   120k allocs (past the 2048 window: the log walk)
```

The last row is the honest cost of the window: a cold whole-history read of
a form with 10,000 patches. The longest board in the author's store holds
99, so it does not happen there, and the fix if it ever does is a bounded
range read in figwal rather than a full walk.

#### Phase 4 landed, and what the attempt taught

`CheckWritable` is wired. A hand-written harness key is refused, on a live
aria and a dormant one, with the same message:

```
$ figaro state set --id <aria> system.cwd /tmp/nope
error: set: jsonrpc error -32000: system.cwd: written by the harness, not by hand
```

Birth, fork and ordinary keys are unaffected. `ApplyFormPrivileged` is the
harness's own path (the boot patch's `system.cwd`), and there is no wire
field for privilege and must never be one.

**The attempt that failed, because the lesson is the valuable part.** I first
made `Agent.Set` synchronous so a live aria could RETURN the refusal instead
of logging it. Two tests failed, and the second was the important one:
`TestFormSetDuringToolRoundAppliesNextRound` hung for the full 30 s timeout.

A `set` arriving mid-turn is applied at the next ROUND BOUNDARY, deliberately.
Waiting for its verdict therefore blocks the caller for the length of a tool
round, and in that test forever. **Synchronous `set` is wrong for a live
aria, and the deferral is a feature, not an oversight.**

The resolution splits the checks by what they need:

- **Protection is a pure function of the patch**, so it is answered at
  ACCEPT time, before queueing, and the caller gets a real error with no
  waiting.
- **A stale `ifVersion` and an `Assert` removal need STATE**, which only the
  writer has. They stay deferred and reach the log. Phase 3's ticket is the
  proper close: the caller gets a handle it may await if it wants, and `set`
  keeps not waiting by default.

#### Fleet, end of session (everything landed)

Same harness again, against the two earlier columns:

| | before WAL | after WAL | after everything |
|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 |
| history build (300 CLI patches) | 4.11 s | 4.98 s | 4.99 s |
| turn wall (12 concurrent) | 4.53 s | 5.49 s | **5.01 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | **46.5 M** |
| `Pss_anon` | 30.6 M | 32.2 M | 30.1 M |
| goroutines | 93 | 80 | 80 |
| heap_alloc | 14.9 M | 16.0 M | **9.2 M** |
| resident form patches | n/a | n/a | 361 |

The memory work shows: **PSS down 12 M and heap_alloc down 6.8 M against the
pre-WAL baseline**, with the durability guarantee added rather than removed.
Turn wall came back most of the way. `resident_form_patches` is visible for
the first time, which is the point of reporting it.

## NEXT STEPS (superseding every earlier list in this file)

Everything above is history. This is the queue.

1. **Phase 3: command, event, ack, session and seq.** The attempt to make
   `set` synchronous proved why this is the right shape: a caller wants a
   HANDLE it may await, not a wait it cannot refuse. It also closes the two
   refusals that currently only reach the log (a stale `ifVersion`, an
   `Assert` removal on a live aria), and it is the whole server side of
   optimistic replication.
2. **`cachedLog.mu` to a published snapshot: DEMOTED, and here is why.**
   `BenchmarkCachedLogReadWhileAppending` measures a reader against an
   appender whose append sleeps 3 ms (standing in for the sync). It reads
   **10.55 ns/op, zero allocs**. The `appendMu` split already took the I/O
   out from under the read lock, which was the whole pathology; what is left
   is a brief cache-update window and it does not show. Do this only if a
   profile says otherwise. The benchmark is the guard: if a change makes
   readers wait on the append again, that number goes from tens of
   nanoseconds to milliseconds.
3. **Phase 6: tombstones and leases.** Needed by 8 and 9, and the tombstone
   is what lets a studied form be deleted at all.
4. **Phase 7: retention as a policy** (N segments), and the type-level rule
   that a channel handing out views may not have one.
5. **Phase 8: the topology form**, replacing `trunks.json`. `internal/trunk`
   dies with it.
6. **Phase 9: derived forms and the libretto**, per the rulings: one
   libretto per studied FORM, `@libretto::<formid>`, shared, refcounted, not
   forked, whole-form only, holding a COPY. Study is a two-participant write
   ordered so every crash over-counts.
7. **Phase 10: the API refactor** and `angelus.hello`.

**Perf work that is known and not done**:

- **Preallocation plus `fdatasync` in figwal.** The only lever that lowers
  the 3 ms per-sync floor. An append changes the file size, so `fsync` and
  `fdatasync` cost the same today; `fallocate` at segment creation makes the
  cheap one available.
- **A bounded range read in figwal** (`RecordsBetween`), so a cold
  `PatchesBetween` below the window is O(range) rather than O(offset). Do
  this BEFORE lowering `form_patch_window`, or the quadratic above bites.
- **306 goroutines on a daemon with 3 live arias.** Still unprofiled.
  `ariastress.sh` arms `FIGARO_PPROF`, but its EXIT trap rests the daemon
  before a profile can be taken, so profiling through it needs the trap
  suppressed or a curl inside the run. The socket lives at
  `$FIGARO_RUNTIME_DIR/pprof.sock`; the live daemon predates profiling being
  armed and would need a restart.

**A cold read below the window now stops at the range's end.** `RangePatches`
visits in index order, so walking past `upTo` finds nothing; without the
stop, every cold read of an early range walked the whole log. Still O(offset)
rather than O(1) because `FormLog` has no bounded range read, which is the
figwal `RecordsBetween` item in the perf list.

### A quadratic waiting to happen, if the patch window is ever tightened

The projection walks IR records in ascending order and asks
`PatchesBetween(prev, cur]` for each. If a board's history exceeds
`form_patch_window`, every one of those reads below the window walks the log,
so a COLD retranslate of such an aria is O(records x history): quadratic.

It does not bite today. The default window is 2048 and the longest board in
the author's store holds 99 patches, so nothing reaches it. But whoever
lowers that knob, or meets a board that writes continuously for a very long
time, will meet this.

The fix is the one already on the perf list: a bounded range read in figwal
(`RecordsBetween(channel, after, upTo)`, a thin wrapper over the segment
index that already exists) so a cold read is O(range) rather than O(offset).
Do that BEFORE lowering `form_patch_window`, not after.

#### Phase 3's seam: Submit and Await

`Form.Submit` returns a `Ticket` and does not wait; `Form.Await(ctx, t)`
parks the caller's own goroutine on the existing broadcast. `Apply` and
`ApplyEffect` are Submit plus Await, so nothing outside changed.

This is the half of phase 3 that lives below the wire, and it is what makes
"a handle you may await" possible instead of "a wait you cannot refuse",
which is the lesson the synchronous-`set` deadlock taught. **A caller that
does not need the version never waits at all**, and that is the only thing
that removes a parked goroutine per writer. The libretto cursor is its first
customer when it exists.

**A bug I introduced and caught in the same minute**: routing `applyEffect`
through the public `Submit` dropped the `priv` flag, so the harness's own
boot patch started being refused. Privilege never reaches the public
`Submit` now; both paths go through an unexported `submit` and the
privileged one is a distinct call site, which is the whole point of not
making it an argument.

Still not done in phase 3: `session`, `seq`, the acknowledgement of a no-op
on the wire, and intent on the RPC beyond the `assert` boolean.

## HANDOFF GATE, passed

Run before the role moved, on the final commit of session 1:

```
nix develop: build + vet + full suite        ok
-race -count=3 on store, actor, figaro       ok
FIGARO_CRASH_TEST=1 crash test               ok
nix build .#default                          ok
```

**Session 1 totals**: 66 commits ahead of main. Phases 0, 1, 2, 4 and 5
complete; phase 3 has its store-side seam (`Submit`/`Await`, intent) and
lacks its wire half (`session`, `seq`, the no-op acknowledgement). Phases 6
to 10 untouched.

Deletions worth naming: `Form`'s write mutex and its sink list, `Kick` and
its six call sites, `keepMu`, `cachedLog`'s lock over the append, and the
buffered-durability story figaro used to have.

The one thing a reader should take away: **figaro's writes are durable
before they are visible, and that cost one fsync per record, recovered
under load by batching and paid for in memory by two new bounds.** The
fleet ends 12 MB lighter in PSS than it began.

#### The tombstone

`Form.Tombstone(reason)` writes `system.tombstone` as an ordinary privileged
patch and seals the form. Three properties, each tested:

- **It is a RECORD.** Subscribers hear the death through the stream they
  already read, and a replay reproduces it. A derived form that must be
  rebuildable from the log cannot learn about a deletion nobody wrote down.
- **It is idempotent.** A delete retried after a crash does not have to know
  whether it got there the first time.
- **It survives a reopen.** The seal is rebuilt from the published state at
  open, so a dead form stays dead without anyone re-declaring it.

**The lease registry is the subscriber set**, and for a single process that
is not a simplification but the whole of it. A durable refcount cannot tell
"still reading" from "died holding a reference"; an in-memory set answers
both, because every holder dies when the process does and a restart is a
clean sweep rather than a TTL to wait out. `Form.Reclaimable()` is
tombstoned-and-unread; `Backend.FormReclaimable` exposes it.

A TTL only covers a holder that is alive but silent, which today is nobody
and later is a node on another machine. When that exists this becomes
`{id, holder, expires}` with a sweep, and nothing above it changes.

**Not wired into the delete path yet**, deliberately: `RemoveLeaf` is the
crash-ordered boundary repair (durable-forms §2) and putting a write in the
middle of it wants its own sitting. That is what remains of phase 6.

## END OF SESSION 1

69 commits ahead of main on `feat/incantations`. Every gate green.

| phase | state |
|---|---|
| 0 figwal sync + flush gate | done, released, pinned |
| 1 `actor.Lazy` | done, fuzzed |
| 2 form on the actor, group commit, fsync | done, crash-tested |
| 3 command/event/ack | **half**: `Submit`/`Await`/`Ticket` and intent below the wire; `session`, `seq` and the no-op acknowledgement above it are not started |
| 4 schema validation | done (`CheckWritable`, `ApplyFormPrivileged`) |
| 5 `SubscribeFrom` | done, reachable through `Backend.SubscribeForm` |
| 6 tombstones and leases | **most**: tombstone and `Reclaimable` done; the delete path calls neither |
| 7 retention policy | not started (needs figwal) |
| 8 topology form | not started |
| 9 derived forms, libretto | not started |
| 10 API refactor | not started |

**Start here**: the NEXT STEPS queue above, then phase 6's last piece
(calling `Tombstone` from `RemoveLeaf`, respecting its crash ordering), then
phase 3's wire half.

**Do not repeat these**: the four traps in the handoff summary; the
synchronous-`set` deadlock; inferring a trim from `patches[0].Version`;
routing a privileged write through the public `Submit`.

**Coordinate**: aria 6c2d7b9f is fixing the displaced-tool_result bug on its
own worktree off this branch, in `internal/figaro/repair.go`,
`internal/figaro/study.go`, `internal/angelus/study_hub.go`, the four
provider encoders and `internal/form/incantation.go`. Stay out of those.

---

# SESSION 2 (aria c1d55d02)

Role `@980dc16c` moved on the handoff; the heartbeat followed it.

### Phase 6 finished: the delete path buries what it takes

`XwalBackend.Remove` now hands `RemoveLeaf` a **bury** hook, called after its
refusal and before any detach or unlink:

- Each doomed form records its own death (`Tombstone`) while its channel
  still exists. A subscriber hears it on the stream it already reads, which
  is the difference between a death and a silence.
- Then the aria's caches are forgotten: handle, form, meta. **Every id in the
  set, not just the one named.** A recursive delete used to unlink a subtree
  and leave its children's forms resident, pointed at files that no longer
  existed, so `FormState` on a deleted aria kept answering from cache.

**The ordering property is the test.** `TestRefusedDeleteBuriesNothing`: a
tombstone cannot be taken back, so burying before the refusal would seal an
aria that is still alive. It failed on the first run, and the reason was not
the ordering.

### The bug that failure exposed: a stale topology decided the delete set

`xwalTopology.Nodes()` read `s.topology.Load()` raw, while `From()` resolves
through `s.Node`, which refreshes. So the adjacency the delete set is built
from could predate the fork it was supposed to include. Consequences, all
observed rather than argued:

- `fig kill <parent>` on a just-forked aria was refused by **figwal**
  (`trunk has 1 live branch(es)`) rather than by figaro, with a message no
  listing can explain.
- `fig kill -r <parent>` unlinked the fork — correct, it is a child — but
  figaro's own delete set never contained it, so its presentation edge, meta
  and caches were all skipped, and its form kept answering from memory after
  its files were gone.

One line: route `Nodes()` through `topologySnapshot()`. When nothing has
moved that is two loads and a compare, and it is the same freshness rule
every other reader of the topology already obeys.

`TestDeleteSetSeesAForkMadeSinceTheLastRefresh` is the guard, and it fails
on the parent commit.

**Deviation from the plan.** durable-forms §7 defers reclamation until a
tombstoned form has no reader. There is no sweep to collect a deferred
unlink, and deferring without a collector converts a tolerated race into an
unbounded disk leak, so the unlink goes ahead and the case is LOGGED
(`tombstone: unlinking a form still being read`). The reader learns from the
tombstone it has just been sent. When the sweep exists, that log line is
where it hooks in.

**Deviation, second.** A tombstone that cannot be written is logged, not
fatal. Deleting is the recovery for an aria whose disk is misbehaving, and
refusing the delete because the death record failed takes that away.

### Design correction folded in (from aria 057ebc2e): fork under-counts

`plans/durable-forms.md` §12.2.2 is new. The libretto refcount ordering was
chosen so every crash OVER-counts; **fork, import and kill are three write
sites outside the study verb that break it in the unrecoverable direction**,
because a child inherits its parent's `study-set` without anything
incrementing the librettos it names. Fork must increment before the child is
created. The reconciliation sweep RECOMPUTES rather than adjusts, so it
repairs under-counts too, which §12.2.1 did not say and which makes it a
backstop for all three.

### `cachedLog` is a published snapshot, and the shadow index is gone

Lock audit item 2. One `RWMutex` over `rows`, `trimmed`, `bytes` and a `byFK`
index became one immutable `logView` behind an `atomic.Pointer`, the pattern
`formState` already uses. Readers take one load and hold nothing; mutators
(append, trim, clear) serialize on `writeMu`, which no reader ever touches.

**`byFK` is deleted outright.** It mapped FigaroLT to absolute index, and it
answered nothing the rows cannot: entries are ascending by FigaroLT (that is
what `ReadFrom` binary searches on), so a resident hit is a search and every
miss already went to the inner log whenever anything had been trimmed. What
it did do was **grow forever** — nothing pruned it on trim — so a bounded
window carried an unbounded index of the entries it had dropped. `Lookup`
now searches for the LAST match, which is what the map's last-write-wins
semantics gave.

Measured, `-benchtime 200x -count=5`, same box, same filesystem:

| benchmark | before | after | |
|---|---|---|---|
| `CachedLogReadWhileAppending` | 11.45 ns | **1.30 ns** | **-88.7%** |
| `OpenWindowed/unbounded` B/op | 7.163 Mi | **5.478 Mi** | **-23.5%** |
| `OpenWindowed/budget=256KiB` | 3.936 ms | 3.875 ms | -1.5% |
| `WindowAppend/window=512` | 257 ns | 207 ns | ~ |
| `CachedLogReadLongAria/10000` | 291.3 µs | 323.9 µs | **+11.2%** |
| `CachedLogReadLongAria/50000` | 1.487 ms | 1.686 ms | **+13.3%** |

The first row is the one the change was made for, and its benchmark says so
in its own comment: a reader mid-append paid the writer's cache update and
now does not.

**The last two rows were NOISE, and re-measuring said so.** Five samples at
200 iterations of a millisecond-scale copy is not a measurement. Re-run from
a base worktree at `-benchtime 300x -count=10`:

| | before | after | |
|---|---|---|---|
| `CachedLogReadLongAria/1000` | 22.29 µs | 22.47 µs | ~ (p=0.85) |
| `CachedLogReadLongAria/10000` | 325.1 µs | 327.7 µs | ~ (p=0.97) |
| `CachedLogReadLongAria/50000` | 1.720 ms | 1.752 ms | +1.86% (p=0.011) |

Geomean +1.15%, and the only significant row is under two percent on a call
documented as the cold path. `Read()` does the same work either way and
allocates identically to the byte, which is what said the +13% could not be
real. **Do not publish a five-sample benchmark**; the base worktree at
`/var/tmp/figbase` makes the ten-sample version cost nothing but patience.

### Phase 3, the wire half: an outcome, not an OK

`SetResponse` gains `Outcome` and `Version`. Three outcomes, because three
things can happen and all three used to be `OK` with an empty list:

- **`applied`** — reduced, appended, fsynced; `Version` is the record.
- **`unchanged`** — legal and changed nothing; `Version` is where the board
  still stands. This is the ambiguity durable-forms §4.1 exists to remove.
- **`queued`** — accepted by a LIVE aria, which applies a set at the next
  round boundary by design. The verdict is not knowable yet and `Version` is
  zero. This is the honest name for the deferral that the synchronous-`set`
  attempt died on last session.

**Deviation: `session` and `seq` are NOT added.** They are for duplicate
suppression across a reconnect and for correlating an ack to an optimistic
client's pending queue. Nothing replicates yet, so a server-side dedup window
would be speculative state with no reader, and wire fields nobody sends are
the opposite of the standing order. The outcome and the version are what the
CLI and a script can use today. When a replica exists, the pair lands with
its dedup window and its conformance test in one change.

**And the CLI stops lying.** `fig set`/`fig unset` print the outcome:
`unchanged: mantra = "x"` when the board already held it, `queued:` when a
live aria will apply it at the next round boundary, and `set … @V` with the
durable version otherwise — which is the number a script quotes back as
`if_version`.

#### Live validation of the outcome, on an isolated daemon

Unbound form (the hub path, no agent):

```
$ figaro state set --id @49f50c5b brief hello
set brief = "hello" (figaro @49f50c5b) @3
$ figaro state set --id @49f50c5b brief hello
unchanged: brief = "hello" (figaro @49f50c5b) @3
$ figaro state delete --id @49f50c5b nosuch
error: set: remove "nosuch": no such key
```

Live aria (the agent path):

```
$ figaro set --id 78c19579 brief hello
queued: brief = "hello" (figaro 78c19579)
$ figaro unset --id 78c19579 nosuch
queued: nosuch (figaro 78c19579)
```

The second one is the point. That refusal is deferred to the round boundary
and reaches only the daemon log, and until now the CLI printed
`unset nosuch (figaro …)` — a write it had not made and would not make. It
now says what it actually did: it queued something.

### The quadratic is gone, and figwal never had to change

`plans/progress.md` (and 6c2d7b9f, from the libretto side) had this down as
"add `RecordsBetween` to figwal". It was not needed. figwal's `Log.Range`
already takes a `from`, and `XWAL.ReadAt` already addresses an arbitrary
index; the waste was entirely in figaro, whose `RangePatches` **started at
record 1 and skipped its way up to the range it wanted**.

`FormLog.RangePatches` now takes `(from, upTo)`. `xwalFormLog` starts the
read AT the range and stops at its end, and `errStopRange` — a sentinel
error whose only job was to abort the walk early — is deleted with it.

`BenchmarkFormColdDelta*`: one small range below the patch window, which is
what a retranslate asks for once per IR record.

| | before | after | |
|---|---|---|---|
| range at 500, history 2000 | 142.7 µs | **3.05 µs** | **-97.9%** |
| range at 1500, history 2000 | 384 µs | **3.4 µs** | **-99.1%** |
| allocs, either | 29 | 29 | unchanged |

The slope is the real result: before, tripling the offset roughly tripled
the cost (O(offset)); after, it is flat (O(range)). A cold retranslate of an
aria with a long board was O(records × history) and is now O(records).

**This unblocks the note that said "do this BEFORE lowering
`form_patch_window`".** The knob is now safe to tighten, which is the
largest remaining memory lever on a form: 2048 decoded patches per resident
board, kept for a cold read that now costs 3 µs from disk.

### The goroutine census, since nobody had run one

The live daemon has no pprof socket (`FIGARO_PPROF` was not set when it
started), so this is an isolated daemon with the profiler armed: 5 arias,
5 unbound forms, then 5 concurrent `form listen` clients, then idle.

```
5 arias                41 goroutines / 5 endpoints
+ 5 forms              48 / 10
+ 5 listeners          46 / 10
listeners gone         46 / 10
20s idle               46 / 10
```

The profile says where they are:

```
10  angelus.(*ariaHub).listen     one per endpoint
10  angelus.(*ariaHub).accept     one per endpoint
 5  actor.Start                   one per resident agent inbox
 5  figaro.(*Agent).act           one per resident agent
```

**Two per endpoint, two per resident agent, and listeners leak nothing** —
the count is identical before and after five of them come and go. So the
355 goroutines on Gluck's daemon (29 endpoints, 4 live arias) are a working
set, not a leak: 58 of them are endpoint accept/listen pairs, the rest ride
live turns.

**The endpoint pair is the interesting one.** It is held for as long as the
node's hub exists, whether or not the aria is awake, so it is a per-node
standing cost that dormancy does not reclaim. Whether a dormant aria needs
its own socket at all is a question for the API refactor (phase 10), where
`node.attach` is already the only method that hands out an endpoint.

**What this does NOT explain** is the memory. The same daemon reports
`heap_alloc` 260 MB against 38 MB of resident IR. Neither bound landed so
far (the IR window, the patch window) touches the other 222 MB, and nothing
in the goroutine census accounts for it either. That is the next thing to
measure, and it should be measured with a heap profile on a daemon that has
`FIGARO_PPROF=1` from the start rather than guessed at.

### The other cache, measured on 515 real arias

The hypothesis was that the unmeasured translation caches were the missing
222 MB. **They are not**, and the probe says so plainly
(`realtrans_probe_test.go`, against a COPY of the real store, 515 arias
opened at once, IR window off):

```
TOTAL   ir = 1,088,582,800 bytes    translations = 85,696,595 bytes   0.08x
largest single aria   ir 11.4 MB / 1609 rows   xlt 2.95 MB / 1606 rows
```

Translations are **8% of the decoded IR**, and about 25% of it on the
longest arias. So the IR remains the dominant term and the 222 MB is still
unaccounted for; it needs a heap profile on a daemon started with
`FIGARO_PPROF=1`, which the running one was not.

**But the comparison that matters is against the BOUND, not against the
IR.** The IR is windowed to 4 MiB per aria; the translations beside it were
windowed to nothing at all. On any aria long enough to matter, the bounded
cache is capped at 4 MiB while its unbounded neighbour keeps growing — so
the unmeasured one becomes the larger half precisely where memory is worst.

So: `[memory] translation_window_mb`, default 4, floored like the IR's, 0
for unbounded. **The default binds nothing that exists today** (the largest
real aria holds 2.95 MiB), which is the point: it caps growth that had no
cap, and it changes no measurement taken so far.

Two tests, at the two ends: `TestTranslationWindowBytesDefaults` for the
three answers a knob owes (unset is bounded, explicit 0 is unbounded, below
the floor is raised), and `TestTranslationBudgetReachesTheCache` for the
thing a config test cannot see — that the budget reaches the CACHE, bounds
residency, and loses no records, because a read below the window still
falls through to the log.

## WHERE THE MEMORY IS. `fig ls` retains 95 MB, and none of it is figaro's

This is the answer to the 2 GB question, and it is not in any cache figaro
owns. Measured, on a daemon started with `FIGARO_PPROF=1` against a COPY of
the real store (515 conversations, 577 trunks, 281 MB on disk):

```
after boot                    heap  ~8 MB
after ONE `figaro ls -j`      heap  208 MB     resident_arias 0
                                              resident_ir_bytes 0
                                              resident_translation_bytes 0
```

Zero resident arias. Zero IR. Zero translations. Every cache figaro
measures is empty, and the heap is 208 MB. The profile (`inuse_space`,
after the call):

```
83.21MB 78.99%  figwal/log.buildOwnSnapshot.func1
95.30MB 90.47%  angelus.(*handlers).list
        chain:  list -> ... -> disk.(*Log).RangeOwn -> buildOwnSnapshot.func1
```

**`figwal/log.buildOwnSnapshot` copies every record's payload into an
in-memory snapshot when a Log is opened:**

```go
err := l.RangeOwn(0, func(idx uint64, payload []byte) error {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	snap.entries = append(snap.entries, cp)
	return nil
})
```

That is figwal's lock-free read cache — the same published-snapshot pattern
this whole changeset has been applying — but **whole-log rather than
windowed**. Opening a node materializes the node's entire encoded history.

**And a listing opens every node.** `handlers.list` asks
`Backend.LastTS(id)` per row for recency, which is figwal's counter on the
OPEN handle, and the comment at `XwalStore.LastTS` says so in as many
words: *"one head open hydrates a cold node, and the trunks layer keeps it
warm"*. It never wakes an agent, which is what the comment was defending,
and it hydrates the store.

So the layering, stated plainly:

| cache | contents | bounded |
|---|---|---|
| figaro `cachedLog` (IR) | decoded entries | yes, `ir_window_mb` |
| figaro `cachedLog` (translations) | decoded entries | yes, as of this session |
| figaro `formState.patches` | decoded patches | yes, `form_patch_window` |
| **figwal `cacheSnapshot`** | **every raw payload of every channel** | **no** |

Every bound this project has added sits on top of an unbounded one. On this
store the ceiling is the store: 281 MB on disk becomes ~281 MB resident
once everything has been touched, before figaro decodes a byte of it.

`handle_idle_minutes` (figwal's `IdleUnload`, 5 min) does reclaim it — which
is why the daemon does not simply grow forever. But **a shell status line
running `fig ls` on a timer re-touches every node faster than the idle clock
can drop it**, and that is the steady state Gluck is looking at: 260 MB of
heap with 4 live arias and 38 MB of reported IR.

### The three candidate fixes, and what each costs

1. **Stop hydrating for recency (figaro, small).** A listing needs "when was
   this node last written", not its history. The newest segment file's mtime
   answers it with one `stat` and no hydration. Changes `fig ls` ordering
   semantics from "newest record timestamp" to "newest write time", which are
   the same thing in every case that is not a restore, so **it needs Gluck's
   ruling before it lands** rather than my judgement at one in the morning.
2. **Bound the snapshot (figwal, large).** A tail window with a fall-through
   to disk below it, exactly what `cachedLog` does one layer up. It is the
   right long-term shape and it costs figwal's "reads are lock-free from
   memory, always" property below the window.
3. **mmap the segments instead of copying payloads (figwal, largest).** The
   page cache becomes the cache, the pages are shared and evictable by the
   kernel, and RSS stops being figaro's problem. Best end state, biggest
   change.

**Nothing here is landed.** The instrument and the evidence are: this
section, `listing_cost_test.go` and `realtrans_probe_test.go`, both env-gated
against a copy. The measurement is repeatable in about ninety seconds and
should be repeated after any of the three.

### And now it is a number: `figwal loaded-heads`

`xwal.Store.LoadedHeads()` was exported and reported by nothing. `doctor mem`
prints it, and the wire response carries it:

```
figwal     loaded-heads=54  (each holds a channel's whole raw history)
heap       alloc=86.0 MiB
```

54 heads, 86 MiB, ~1.6 MB each, after one listing on the 515-aria copy.
Note it is 54 and not 515: a listing does not hydrate every node, and
whatever selects those 54 is the next thing to understand if fix 1 is taken.

The live daemon cannot answer yet — it is running a binary that predates the
field — but **any daemon started after this commit can be asked where its
memory is without a profiler**, which is the whole point. `resident_ir_bytes`
next to `loaded_heads` is the comparison that matters: the first is figaro's
bounded cache of decoded entries, the second is figwal's unbounded cache of
raw ones, and the second is an order of magnitude larger.

### Which call hydrates, refined (and one of my own numbers corrected)

An exact memory profile of the listing test (`-memprofilerate 1`) puts
figaro's own retention at 7.35 MB, **87% of it in `OpenForm`**:

```
6.43MB 87.45%  store.OpenForm
3.53MB 48.05%  encoding/json.(*RawMessage).UnmarshalJSON   (under it)
0.39MB  5.35%  store.topologySnapshot
```

So the hydrator is the **OUTFIT column**. `labelOf` falls back to the board
for any node with no stump — 209 of 515 in the real store — and
`FormState` opens the Form, whose replay opens the NODE, which is what makes
figwal build that node's whole-log snapshot. `topologySnapshot` itself is
0.39 MB: `vectorsLocked` and `presentLocked` are pure in-memory walks, and
`ListLight` was already fixed for this exact reason once before.

**Correcting myself**: I read "+113 MiB in `Conversations()`" off
`ReadMemStats` deltas and attributed it to the topology build. The profile
says otherwise. Both were measured; only one was measured at the right
granularity, and the coarse one attributed the cost to whichever call
happened to straddle a GC. The daemon-side profile (83 MB in
`buildOwnSnapshot`, reached through `handlers.list`) is the trustworthy one,
because it was taken from a living daemon rather than inferred from deltas.

### So the choice is latency against memory, and it is Gluck's to make

The label is two keys. Getting them costs a form replay and a node
hydration, per row, per listing.

1. **Do not RETAIN the form opened for a label.** Purely internal, no
   second source of truth, and it extends a doctrine already in the code
   ("TOUCH IS USE, NOT SIGHT" at `seenLocked`) from the idle clock to the
   registry. **Costs listing latency**: a status line re-replaying 209 forms
   every few seconds is real CPU, and the current design deliberately chose
   the other way.
2. **Cache the label in the meta sidecar**, which a listing already reads
   for message counts and tokens. No replay, no hydration, and the label is
   exactly as stable as the counts beside it. **Costs a second source of
   truth** for state that lives in the form, which this design generally
   forbids — mitigated by the heal path that already exists
   (`meta_heal.go`) and by the fact that `b.labels` does precisely this for
   STUMP labels today, justified there because a stump id is the hash of its
   own content.

I have not taken either. Both change something a user can feel (`fig ls`
latency, or where a label comes from), and the measurement is now cheap
enough that either can be judged in ninety seconds:

```
box=$(mktemp -d); cp -a --reflink=auto ~/.local/state/figaro/arias $box/arias
FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run ListingCost -v
```

### Phase 8 landed: the hierarchy is a form

`internal/trunk` (296 lines) and `trunks.json` are gone. The presentation
hierarchy is `store.TopologyTree`, a `store.Form` on a reserved stump.

**Why a stump.** The design wanted an unbound form NODE with a well-known
id, which needs client-specified trunk ids, which figwal does not have (and
which Gluck has already asked for: `answers-forms.md` §2). A stump is the
one node figwal names by a caller-chosen string, so the form needs no marker
file and no lookup, cannot be forked and cannot be bound. `listStumps`
filters it out, or the hierarchy grows a row describing itself.

**What it buys, checked rather than claimed:**

- A promote is **one patch naming two edges**
  (`TestTopologyForm_PromoteIsOneRecord`). The file it replaces rewrote the
  whole document per edit and could half-land.
- Durability, versioning and the single writer are the form's: reduce, append,
  fsync, publish. `Rev()` is the form version.
- `trunk.mu` is gone — the lock audit's fifth entry.
- **Parity is the old package's own test suite, ported verbatim**: same
  diagram, same claims, same names. That is what makes "it behaves as the
  file did" checkable.

**Migration**: a legacy `trunks.json` is folded in on first open and renamed
`.migrated`. Ordering rather than a journal — the fold is idempotent, so a
crash before the rename replays harmlessly, and a form already holding edges
is never migrated into.

**Live, on an isolated daemon**: `fig ls` draws parent→child, `promote`
swaps them, `ls -g` shows **no** `@topology` row, and the promote survives a
daemon restart (replayed from the channel, not re-read from a file).

**Deviation: retention is not built.** durable-forms §8 says the topology
form compacts to a single segment, which needs figwal's compacting channel
(phase 7). Until then it keeps every record: correct, and unbounded. Promotes
are rare so the growth is slow, but it IS growth, and phase 7 should point
here first.

| phase | state |
|---|---|
| 0,1,2,4,5 | done (session 1) |
| 3 | **wire half done**: outcome + version; `session`/`seq` deliberately not built |
| 6 | **done**: tombstone, `Reclaimable`, and the delete path calls both |
| 7 retention | not started (needs figwal) |
| 8 topology form | **done**, live-validated, `internal/trunk` deleted |
| 9 libretto | not started; §12.2.2 records a design bug found before it was built |
| 10 API refactor | not started |

### The OUTFIT column stops re-opening forms

A memo for the label, invalidated by any write that names an outfit key,
registered on the form's commit sink where it is OPENED — so every writer
passes it: the hub, the agent's own loop, a birth dressing, an outfit fold.
The board stays the only source of truth; this is a cache of it with a
complete invalidation, not a second copy on disk.

**The cycle it breaks.** A listing reads a label per row, which opens a Form,
whose replay opens the node, which figwal answers by materializing the whole
channel. A form is then evicted for idleness at 15 minutes, and the next
listing opens it again. A status line on a timer therefore cycles the store
through memory forever. Measured on the real-store copy:

```
evict every form, then list again:
  before   209 forms re-opened
  after      0 forms re-opened      (resident_form_patches 0, heads unchanged)
```

**What it does NOT fix, said plainly**: the FIRST listing still costs +114
MiB, and that arrives on the topology build rather than on the labels. Two
instruments disagree about which call triggers it — the daemon's own heap
profile says `buildOwnSnapshot` under `handlers.list`, and the in-test
`ReadMemStats` deltas put it on `Conversations()` — and I have already been
wrong once by trusting the coarse one. Whoever takes it next should isolate
the topology build under a profile rather than a delta.

**A bug in my own instrument, since it wasted three runs**: `HeapAlloc` can
FALL between two readings when a GC collects more than the step allocated,
and an unsigned subtraction renders that as 17592186044416 MiB. The helper
now returns `int64` and prints signed deltas.

### Fleet regression after this session (same harness, same box)

`scripts/ariastress.sh --arias 12 --study --study-patches 300`, against the
three columns session 1 left:

| | before WAL | after WAL | end of session 1 | **end of session 2** |
|---|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 | **12/12** |
| history build (300 CLI patches) | 4.11 s | 4.98 s | 4.99 s | 5.14 s |
| turn wall (12 concurrent) | 4.53 s | 5.49 s | 5.01 s | 5.85 s |
| control (12 x `ls -j`) | 0.17 s | 0.16 s | — | **0.16 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | 46.5 M | 48.3 M |
| `Pss_anon` | 30.6 M | 32.2 M | 30.1 M | 31.8 M |
| goroutines | 93 | 80 | 80 | **80** |
| heap_alloc | 14.9 M | 16.0 M | 9.2 M | 10.5 M |
| resident translation bytes | — | — | — | **194,967** |

**The control is unchanged**, which is what licenses reading the rest.
History build +3% and PSS +1.8 M are the new residents: the topology form
(one more open form for the daemon's life), the label memo, and the
translation accounting that now actually measures itself. Goroutines flat.

**Turn wall is +0.84 s and I do not claim it as a regression.** It is
dominated by provider round trips, it moved 5.49 → 5.01 between two runs of
the SAME build in session 1, and nothing in this session touched the turn
path. If it matters, it wants the twelve-aria recipe run three times on one
build before anyone reads a number off it.

The last row is new because nothing could report it before: 60 translation
rows, 195 KB, on arias with 72 IR rows between them.

### A negative result, and it is worth more than the change was

**Hypothesis**: a form's cold open replays every record to rebuild a value
figwal already holds folded (the form channel is reducible, and a segment
header carries the fold). So take `StateAt(last)` for the snapshot and read
only a bounded TAIL of patches, and a cold open becomes O(window) where it
was O(history).

I built it, with `FormLog.Bounds` and `FormLog.FoldedAt`, both real logs
implementing them, the `trimmed` trap handled, and a `TestColdFoldEqualsReplay`
equality suite (fold and replay must be indistinguishable: same version, same
state, same `PatchesBetween` answers at every range). All of it passed,
including the stump-hosted topology form.

**Then I measured it. `BenchmarkFormOpenReplay`, 6 samples:**

| | before | after | |
|---|---|---|---|
| M=30 / N=100 | 17.78 ms | 23.65 ms | **+33%** |
| M=30 / N=2000 | 26.17 ms | 114.66 ms | **+338%** |

**Reverted in full.** Two reasons, and the second is the interesting one:

1. Three calls (`Bounds`, `FoldedAt`, `RangePatches`) each opened the node
   handle, where the replay opened it once. That part is fixable.
2. **The replay was never reading disk.** By the time figaro asks for the
   first record, figwal has already copied every payload of the channel into
   memory (`buildOwnSnapshot`, see "WHERE THE MEMORY IS"). So figaro's
   "replay" is a walk over RAM, and there is no I/O for a fold to save. The
   optimization was aimed at a cost that does not exist at this layer.

**What that means for the next person, and it is the useful part:** no
figaro-side change to how forms open can pay for itself while figwal
materializes whole channels. The cold-open cost IS the hydration. Fix it in
figwal — 6c2d7b9f's segment-granular lazy loading, sketched in their message
and worth building — and only then revisit the fold, which becomes a genuine
saving the moment reading a record can miss.

The equality test was the right instrument and it did its job: it proved the
change CORRECT, and the benchmark proved it not worth having. Both were
needed, in that order.

### The steady-state listing is now free

Recency was the second per-row hydrator. `LastTS` is answered from figwal's
counter on the OPEN handle, so a cold node is hydrated to answer it — the
same disease as the OUTFIT column, and the same cure: memoize, and let the
writes invalidate.

Invalidation is complete because every append this daemon makes passes one
of two points: the IR log handed out by `Open` (wrapped in a one-method
`recencyLog` decorator) or a Form's commit sink. A delete drops both memos
with the aria's other caches.

Measured on the real-store copy, listing every row exactly as `handlers.list`
does (label + recency), then evicting every form and listing again:

```
first listing    +115 MiB   216 heads loaded
SECOND listing    +0.0 MiB    0 forms re-opened   216 heads (unchanged)
```

**A status line on a timer now costs nothing.** That was the actual
complaint: not that a listing is expensive once, but that it was expensive
every few seconds forever, because the caches it filled were evicted and
refilled in a cycle.

**The first listing still costs 115 MiB and that is figwal's**, not
figaro's: opening any node copies the whole channel into memory. Nothing on
this side can fix it (see "A negative result"), and the fix is
segment-granular lazy loading in figwal.

### PR 16 taken, after validating it on my own harness

`8d330026` (the translator skips derivation for a record it has already
encoded, plus the splice carrying `FormVersionOfSnapshot` AND
`LastStudyVersions`) merged into `feat/incantations` at `8f1a6853`.

What I checked, because Gluck asked for it on my harness rather than theirs:

| leg | result |
|---|---|
| `internal/provider` Observation suite, 6 samples | no regression; `Warm8` **-19.5%** (p=0.009) |
| long history (2000-turn IR, 1 and 8 forms), added to BOTH trees | `LongCold` **-6.5%** (p=0.041), long warm unchanged |
| full suite, vet, `-race -count=3` on store and provider | green |
| `nix develop`, fresh | build and full suite green |
| real store, fresh copy | 653 nodes, 647 forms, 5935 patches; warm delta reads still **35-36 ns/op, 0 B/op** |
| ad hoc, visually checked | fork, promote, delete, normalize all correct |

**Their -63% is not reproducible on my harness and must not be quoted from
it.** These benchmarks carry no per-LT translation cache, so the cache-hit
path their change optimizes is never taken. My harness proves the other
half, which is the half a merge needs: nothing around it got slower.

The visual leg is worth keeping as a recipe, because it exercises phase 8
end to end at the same time: two forks draw nested, a promote climbs, a
delete repairs the boundary (the survivor absorbs its prefix and its FORK
column goes to `-`), a second delete of the same id errors cleanly,
`normalize` reports already-normalized, the survivor's board is intact, and
`loaded-heads` is 0 afterwards. `/var/tmp/figstate/prvisual.sh`.

**Pushing to origin is Gluck's call.** The merge is local; the PR stays open
until he says otherwise.

### The config test the plan asked for, and it catches an unwired knob

durable-forms §9 asks for "a config supplying all four, read through the
loader, asserted at each enforcement point, not at the parser". It did not
exist. It does now, and it needed a small extraction to be possible: the
daemon set its knobs inline in `runAngelus`, so nothing could test the trip
from a config FILE to the place a value is enforced.

`applyStoreSettings` and `applyCacheSettings` are that boot step, extracted.
`TestMemorySettingsReachTheirEnforcementPoints` writes a real `config.toml`
with all six memory knobs, loads it through `config.Load`, applies them the
way the daemon does, and asserts the package variables the store consults
when it opens a handle, builds a form, or trims one.

**Proved red before green**: unwire `SetPatchWindow` and it fails with
`form patch window at the enforcement point = 2048, want 64`. That is the
class of bug this project has shipped twice — the IR window defaulted to off,
figwal's `IdleUnload` read nothing — and a parser test cannot catch either,
because the parse was never the broken half.

`HandleIdleForTest`, `FormLingerForTest` and `PatchWindowForTest` exist only
to let the test read those enforcement points from another package.

### `fig set --wait` (Gluck's ruling, 2026-08-13)

The one place the two halves of a write disagreed: on a LIVE aria a stale
`ifVersion` and an `Assert` removal are answered by the writer, which runs at
the next round boundary, so they reached the daemon log and not the caller.

`--wait` asks for that verdict. The event carries a buffered channel, the
round boundary fills it, and the caller gets `applied`/`unchanged` plus the
version. **Both** places a queued set can be applied report it — the drain
loop and `serviceSets` at the boundary — or a waiter hangs on a turn that
already applied its patch.

**Opt-in, and the tests are what keep it so.** Live, on an isolated daemon:

```
$ figaro set --id a5edb50e brief one            # default, unchanged
queued: brief = "one" (figaro a5edb50e)
$ figaro set --id a5edb50e --wait brief two
set brief = "two" (figaro a5edb50e) @6
$ figaro set --id a5edb50e --wait brief two
unchanged: brief = "two" (figaro a5edb50e) @6
```

Three tests: the DEFAULT set does not block during a tool round (the deadlock
the first attempt shipped, asserted as an absence), `--wait` does block and
answers correctly when the round ends, and a caller whose context expires
stops waiting while the patch still lands. `TestFormSetDuringToolRoundApplies
NextRound` — the test that hung the first attempt for its full timeout — is
green.

---

## THE NEXT WORKER'S FIRST JOB: figwal segment-granular lazy loading

Gluck's ruling, 2026-08-13: **figwal version bumps are expected**, to be
coordinated properly, with `nix build` proven to work. So the objection that
kept me off this is gone, and it is handed over as the first job because it
is the largest single memory lever in the system.

### The problem, with the evidence already gathered

`figwal/log.buildOwnSnapshot` copies **every record's payload** of a channel
into an in-memory snapshot when a Log is opened:

```go
err := l.RangeOwn(0, func(idx uint64, payload []byte) error {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	snap.entries = append(snap.entries, cp)
	return nil
})
```

That is figwal's lock-free read cache — the same published-snapshot pattern
this changeset applies everywhere — but WHOLE-LOG rather than windowed. So:

- one `fig ls` on the 515-aria copy retains **95 MB**, with zero arias
  resident by figaro's own count (`213d290f`, and the profile is in "WHERE
  THE MEMORY IS");
- the ceiling is the store: 281 MB on disk becomes ~281 MB resident once
  everything has been touched, before figaro decodes a byte;
- **every bound this project added sits on top of it** (`ir_window_mb`,
  `translation_window_mb`, `form_patch_window` all bound DECODED copies of
  bytes figwal is already holding raw).

### 6c2d7b9f's design, which I endorse

One flattened per-channel segment index built at open by readdir over the
lineage, entries `{base, path, visibleTo}`; a store-wide `sync.Map` from
segment PATH to a slot holding `atomic.Pointer[bytes]` + `usedAt` + a load
mutex, so one file has one copy however many lineages pass through it;
eviction nils the pointer and never touches the map; within a segment,
lookup is `entries[lt-base]`, O(1).

### What I learned that changes how to approach it

**Do not micro-optimize figaro first.** I built the version-addressed cold
open (fold from the reducer instead of replaying) with a full equality
suite, and it measured **+338%**, because the replay was never reading disk:
figwal had already copied everything into RAM. Reverted. The lesson
generalises — no figaro-side change to how anything OPENS can pay for itself
until reading a record can miss.

### The coordination this needs

1. Land it in `/home/gluck/dev/figwal`, with its `crashtest` harness run
   deliberately, not just the unit tests.
2. Release and pin: figaro currently pins
   `v0.16.2-0.20260813012153-4f9ce6a665f6`.
3. **Reset the flake's `vendorHash`** — edit `go.mod`, build, take the
   "got:" value. Documented in `flake.nix`; the last reset produced
   `sha256-y0FdOfhVnIIOXAXsI9S/wg+9aXQbRne/K4sLGZZdzD4=`.
4. `nix build .#default` MUST pass before it is called done.
5. Re-measure with the instruments already built:
   `FIGARO_PROBE_ROOT=<copy> go test ./internal/store -run ListingCost -v`
   (reports loaded heads and the per-listing heap), `doctor mem`'s
   `figwal loaded-heads` line, and the daemon heap profile recipe in
   `/var/tmp/figstate/heap2.sh`.
6. `scripts/ariastress.sh --arias 12 --study --study-patches 300` before and
   after, against the table in "Fleet regression".

### After it, in order

1. **Phase 7, retention** — and point it at the topology form first, which
   grows one record per promote forever today (my deviation, §8 of
   durable-forms wants a single segment).
2. **Revisit the cold-open fold**, which becomes a real saving the moment a
   record read can miss.
3. **Phase 9, the libretto** — read durable-forms §12.2.2 first: fork,
   import and kill are refcount participants and the design did not say so.
4. **Phase 10, the API refactor.**

### Succession (session 2)

- **Successor minted**: `d604c755`, briefed with the reading order, the four
  traps, the file boundary with 6c2d7b9f, and the figwal job. Its id is in
  `/var/tmp/figstate/.successor`.
- **The role is `@980dc16c`.** Move it with
  `figaro state set --id @980dc16c target-aria d604c755` — a form patch the
  hub serves with no agent involved. **Do not use `fig cast` from inside an
  aria**: it rides the inbox, the inbox is running the turn that issued it,
  and it hangs until the timeout.
- **c1d55d02 stays alive as a reference** at Gluck's instruction: parenting,
  not merely handing over. Ask it; it answers.

---

# SESSION 3 (aria d604c755)

Role `@980dc16c` moved on the handoff; the heartbeat followed it.

## figwal opens an index, not a history (the first job, done)

`figwal 2ff5647` + `3a14131f` + `084418c7`.

**The finding that reshaped the job.** The disk layer was ALREADY lazy. A
`segment.Segment` retains an offset per record and reads payloads by
`codec.ReadFrame(io.ReaderAt, ...)` — a positional pread, one syscall, no
seek state. The only thing making a channel resident was `log.buildOwnSnapshot`
copying every payload at `Open`. So the fix was to stop copying, not to build
a loader: **762 lines added, 368 deleted, and the deletions include
`cacheSnapshot` and its entire parallel implementation of read, range,
scan-from-end, the fork truncation and the parent chain** — every one of
which duplicated logic `disk.Log` already had.

**The measurement that decided the shape**, taken BEFORE any code, because
figwal already had both paths (`Range` reads the snapshot, `RangeOwn` reads
the files):

| | from the snapshot | from disk |
|---|---|---|
| point read | 5 ns | 1.0–2.1 µs |
| replay 2000 records | 3.1 µs | 1.2–2.3 ms |

So **pure laziness was never an option**: a form's cold open replays its
whole channel, and 400x on that path is the +338% lesson from the other
direction. Segment granularity it is.

### What landed in figwal

- `segment` gains a lazily loaded payload block per segment behind an
  `atomic.Pointer`, charged against a process-wide budget (32 MiB default,
  `SetCacheBudget`), evicted least-recently-used when a load crosses it.
  Evicting can lose nothing: the file has every byte, and a caller holding a
  payload from an evicted block keeps it alive by ordinary GC.
- **The active segment's block is EXTENDED by `Append`**, not invalidated, so
  a writer's own tail stays resident without a reload per record.
- `log.Log` keeps only the PENDING buffer — records appended and not yet
  synced, which have no segment to be read from — and delegates everything
  else. The pending window is captured before each disk walk and bounds it,
  so a concurrent sync can neither duplicate nor drop a record mid-iteration.

Measured after, same fixture:

| | before | after |
|---|---|---|
| point read, warm | 5 ns | **17 ns** |
| point read, not resident | 1–2 µs | **13 ns** |
| replay, per record | 1.5 ns | **6 ns** |
| open | whole history resident | **nothing** |

A warm read costs three times an index into an array that held everything; a
read that misses is a hundred times cheaper than it was.

### Two semantic changes, both deliberate, both tested

1. **A `Snapshot` no longer pins payloads.** Records SYNCED before the
   capture and then moved by a fork are served by their new owner, not by the
   stale handle. Unsynced records are still pinned, because the pending
   buffer holds them — which is how the first draft of the test passed for
   the wrong reason and taught me the distinction.
   `TestSnapshotAcrossAForkServesTheKeptPrefix` states it. The guard is the
   one that already existed: a topology mutation demands a private log
   (`ErrSharedMutation`).
2. **`Store.Evict` now drops the evicted log's payload blocks.** Before, an
   idle unload released the log wrapper while the disk store kept the
   segments open — and with them, every byte. The unload was not reclaiming
   what everyone assumed it was.

**A bug fixed in passing**: a forked child was built as
`&Log{inner: childInner}`, leaving `maxLag` at zero, so **every write to a
fork synced inline** regardless of the configured lag.

### The figaro side

`[memory] segment_cache_mb`, default 32, 0 to hold nothing. It is the bound
the other three sit on: `ir_window_mb`, `translation_window_mb` and
`form_patch_window` all cap DECODED copies of these same bytes.

`doctor mem` prints `segment-cache=X of Y` beside loaded-heads, and the wire
response carries both. **Loaded heads stopped being a proxy for memory** —
the count is unchanged at 217 on the real store while the bytes fell by
more than half, because a head now costs its index.

`TestMemorySettingsReachTheirEnforcementPoints` covers it, and it is the
first knob that test checks which is enforced in ANOTHER MODULE. Proved red
first: unwire the call and it reports 32 MiB where 9 was configured.

### The numbers, on a copy of the real store (515 arias, 590 trunks, 281 MB)

Same `ListingCost` probe that found the problem:

| segment budget | heap retained by one full listing |
|---|---|
| unbounded (before) | **116.5 MiB** |
| 32 MiB (default) | **48.0 MiB** (31.9 held by figwal) |
| 4 MiB | **17.6 MiB** (3.9 held by figwal) |

**Residency is a dial now, and it tracks the knob to a tenth of a MiB.**

Live, on a daemon with `FIGARO_PPROF=1` against that copy
(`/var/tmp/figstate/lazylive.sh`):

```
one full listing     2.66 s   heap 73.7 MiB   segment-cache 31.4 of 32.0 MiB
second listing       0.031 s  heap 79.1 MiB   (the status-line case)
```

The predecessor measured 208 MiB of heap after one `ls -j` on the same copy,
and Gluck's live daemon sat at 260 MiB. The first listing is still 2.7 s
because opening a node still SCANS every segment file to build its offset
index; that is unchanged by this work and is the next lever (a segment
footer index would remove it).

### Validation gate

```
figwal:  go test ./log ./disk ./segment ./xwal -race -count=3    ok
         crashtest, short                                        ok
         crashtest -long -seed=11 (113 s)                        ok
figaro:  go build, go vet, go test ./... -count=1                ok
         nix build .#default (vendorHash reset, 084418c7)        ok
         fleet: 12/12 answered, three runs                       ok
         live daemon on a copy of the real store                 ok
```

Fleet, against the four columns above (three runs of THIS build):

| | end of session 2 | session 3, x3 |
|---|---|---|
| turns answered | 12/12 | 12/12, 12/12, 12/12 |
| history build | 5.14 s | 4.93 / 4.96 / 4.94 s |
| turn wall | 5.85 s | 4.98 / 4.77 / 4.52 s |
| control | 0.16 s | 0.16 s, all three |
| daemon PSS loaded | 48.3 M | 49.6 / 49.0 / 59.1 M |
| goroutines | 80 | 80, all three |
| heap_alloc | 10.5 M | 13.7 / 10.5 / 13.1 M |

**heap_alloc spreads ±3 M across three runs of one build**, so neither a
regression nor an improvement can be read off it on this harness; the
12-aria fleet is too small for the segment cache to matter (its arias hold
72 IR rows between them). The real-store numbers above are where this change
is visible, and the control being identical three times is what licenses
reading the rest.

### A bug in the evictor, found by reading my own code (`figwal 09682d7`, `34b493ec`)

The evictor CASes the block pointer it read. A failed CAS means an append
extended the block underneath it — and the first version deleted the segment
from the held set anyway, with its bytes still on the counter. Nothing could
evict that segment again, so **the budget shrank permanently every time the
race was lost**.

The decision moved into `dropLocked`, which reports whether it won, so the
case is testable without a hook: install a block, extend it with an append,
hand the evictor the stale pointer. Red before green.

**The concurrent test beside it never hit the window in fifteen runs under
`-race`**, which is why the deterministic one exists — and is the reason to
distrust "I wrote a concurrent test" as evidence for anything narrow.

A second lesson, from the same test: `Segment` is documented as unsafe for
concurrent use and my first test raced it against itself. In production
`Append` and `ReadIndex` are serialized by `disk.Log`'s RWMutex; the EVICTOR
is the only thing that touches a segment from outside that lock, so that is
the only interleaving worth writing.

### Deviations

1. **The budget is process-wide, not per backend**, because one segment file
   has one copy however many lineages read through it. `doctor mem` says so.
2. **`figwal` was pushed to `origin/master` and pinned by pseudo-version**
   (`v0.16.2-0.20260813071931-2ff564775899`), the same dance as the last
   bump, per Gluck's 2026-08-13 ruling that figwal bumps are expected. No
   tag was cut; say the word and one will be.
3. **32 MiB default**, chosen so the whole author's store (281 MB) cannot be
   held, while a busy fleet's working set can. It is a dial, and the probe
   measures the trade in ninety seconds.

## The second half: a sealed segment is a file, not an open handle

`figwal c07afc2` + `3780bcc`, pinned at `8c0a365f`. Gluck asked why the full
lazy plan had been split; this is the other half, and the split is recorded
above the numbers because the numbers justify only part of it.

Opening a log opened every segment it had, and opening a segment scans the
whole file — every frame, every CRC — to build its per-record offset index.
Only the newest is opened now (it takes the appends and is the only one that
can carry a torn tail); the rest are identified by NAME and opened by the
read that lands in them, because a segment file is named for its base index,
so segment i covers `[base_i, base_{i+1} - 1]` and routing needs no open at
all.

| a 32-segment log, 4000 records | before | after | |
|---|---|---|---|
| open | 5.42 ms / 2.20 MB / 9118 allocs | **0.19 ms / 73 KB / 618 allocs** | **-96%** |
| open + read one record | 5.55 ms | **0.45 ms** | -92% |
| open + read everything | 8.45 ms | 8.64 ms | +2% |

**Deferred with the scan: the CRC check it performs.** Corruption in a
segment nobody reads is found when somebody reads it rather than when the log
opens. A torn TAIL is unaffected — only the active segment can have one.

**Fork does not go near this.** `materializeLocked` opens everything before a
fork plans anything, because fork is the most crash-fragile code in figwal
and an on-demand open in the middle of a committed plan is not a trade worth
making.

### Two of my claims for it were wrong, and the measurements killed both

1. **"It will cut the memory."** It moved the daemon-day figure 48.7 → 48.6
   MiB. A per-record offset is eight bytes against payloads averaging a
   kilobyte; the index was never the mass. It pays in TIME and in file
   descriptors (the live daemon holds 539, one per segment of every open
   head), and that is the whole of it.
2. **"It will cut the 2.7 s first listing."** It did not move at all. In
   process the listing is ~390 ms (topology and labels 302 ms, recency 84
   ms), so the rest lives between the CLI and the daemon's list handler and
   is unprofiled. I had written the segment scan into `reclamation.md` as the
   cause; that note now says the opposite and names the measurement.

## WHAT A DAEMON'S DAY COSTS (the before/after Gluck asked for)

`internal/store/daemon_day_test.go`, env-gated against a COPY, run on the
merge base (`d8428ee1`, old figwal) and on this branch. Identical work,
identical head counts at each phase:

| phase | base alloc / sys | **after** alloc / sys | heads |
|---|---|---|---|
| open | 0.7 / 7.5 | 0.7 / 11.5 | 0 = 0 |
| topology | 118.6 / 195.2 | **48.7 / 95.2** | 217 = 217 |
| listing (label + recency) | 118.6 / 199.2 | **48.7 / 111.2** | 217 = 217 |
| **touching every board, decoding nothing** | **297.0 / 419.2** | **68.5 / 127.2** | 585 = 585 |
| visiting every aria (50,793 IR entries decoded) | 395.4 / 603.1 | 326.9 / 487.1 | |
| after evicting figaro's own caches | 123.7 / 601.6 | 55.1 / 485.6 | |

**Merely LOOKING at every board — no decoding, no rendering — cost 297 MiB of
heap and 419 MiB reserved from the OS. It now costs 68.5 and 127.** The
visiting row is dominated by figaro's own decoded IR, which is bounded
already and unchanged by this work; the row above it is figwal's footprint
alone, and it is the row this changeset exists for.

The last row is the honest cost of the new cache: **an idle daemon retains up
to the segment budget** (32 MiB) where the old one retained whatever its
heads still held. figwal's 5-minute head unload drops it (`Store.Evict` now
releases the blocks), but the cache has no idle clock of its own — worth one,
and it should borrow the head-unload clock rather than invent a fourth
number.

### The live fleet, measured while all this was running

Gluck asked what his own daemon holds. It is `figaro 0.24.3` from nix, up
eight hours, predating every fix here:

```
daemon pid 3168889   PSS 536.1 MB   RSS 562.0   anon 532.6   539 open fds
                     heap alloc 429.5 MiB  inuse 445.7  sys 608.2
                     live=4 resident=10 endpoints=107 goroutines=551
                     ir cache 9101 rows, 44.4 MiB
7 attached CLIs      ~25 MB PSS each (~50 MB RSS, mostly shared text)
TOTAL                1731 MB of PSS across 33 figaro processes
```

**The "2 GB" is a FLEET, not a process**: one daemon at 536 MB, a second at
129 MB, thirteen more between 17 and 59 MB, plus the CLIs. `GOMEMLIMIT`
applies per process, so it never bit; the aggregate has no ceiling at all.
429 MB of that daemon's heap against 44 MB of instrumented cache is exactly
the shape "WHERE THE MEMORY IS" describes, on a binary from before the fix.

## The read-path comparison, and a +475% that was not real

benchstat, 6 samples, merge base (`d8428ee1`) against this branch, on an
otherwise idle box. Everything flat except one row:

| | base | after | |
|---|---|---|---|
| `FormOpenReplay` (6 shapes) | — | — | ~ all, geomean +0.4%/record |
| `FormDeltaPerSend1000` | 58.6 ns | 60.4 ns | ~ (p=0.24) |
| `FormWholePerSend100` | 46.3 ns | 44.7 ns | ~ (p=0.42) |
| `CachedLogReadLongAria` 1k/10k/50k | — | — | ~, +1.6% on 50k |
| **`FormColdDeltaAt500` / `At1500`** | 2.96 / 2.87 µs | **17.0 / 16.7 µs** | **+475% (p=0.002)** |

The cold read below the patch window is the path a previous session took from
142 µs to 3 µs, so a five-fold loss there had to be understood before
anything shipped.

**It is a benchmark artifact, and the instrument that proved it is now
permanent.** The suspect was cache thrash — a block dropped and reloaded per
read. `segment.CacheLoads()` (added for this, kept for good) answered it: 50
cold reads caused **one** load. So the cost was one-time, and the reason it
showed up is that it MOVED: the old code materialized the whole channel
during the untimed fixture build, and the new code loads the segment inside
the first measured iteration. At `-benchtime 100x` that one-time cost is
amortized over a hundred iterations and reads as +14 µs each.

At `-benchtime 4000x`, three samples each:

| | base | after | |
|---|---|---|---|
| `FormColdDeltaAt500` | 2.34–2.47 µs | 2.74–3.03 µs | **+18%**, 29 allocs both, +68 B/op |

So the honest number is **+18% on the cold path**, which is a read going
through an RWMutex and a segment lookup instead of one atomic load of an
array that held the entire channel. That is the trade, stated plainly.

**A rule this cost an hour to relearn**: `-benchtime 100x` on a path with any
one-time cost measures the one-time cost. My predecessor's note says do not
publish a five-sample benchmark; the companion is do not publish a
hundred-iteration one either, unless you have checked that the loop is what
you are timing.

**Bisected between my own two figwal changes**, because it mattered which:
the cost arrived with the payload cache (the `cacheSnapshot` deletion), and
lazy segment opening slightly IMPROVES it (10.5 µs against 11.4 µs at 200x).

## Two faults in my own cache, and the second was Gluck's question

`figwal e44a843`, pinned at `c2347989`.

**1. Recency was a global counter stamped on every read.** An atomic
read-modify-write on one cache line shared by every reader in the process.
Measured with a parallel benchmark written for the purpose:

| cached read | 1 core | 16 cores |
|---|---|---|
| counter per read | 26 ns | **47 ns** |
| epoch (now) | 26 ns | **38 ns** |
| the old whole-channel snapshot | — | **24.8 ns** |

**Reads got SLOWER the more readers there were**, which is the signature of a
contended atomic and the thing a single-threaded benchmark can never show.
Recency is now an EPOCH that advances when a block is loaded and when a sweep
runs — both rare — and a reader stores it only when its segment's stamp is
stale.

**The honest ledger for a warm read**, against the version that held every
payload of every channel in RAM: **2.7 → 15 ns serial, 24.8 → 38 ns
parallel.** Bought with it: residency bounded instead of unbounded, and a
read that misses at 13 ns instead of 1–2 µs. The remaining gap is the disk
log's RWMutex; making sealed reads lock-free is possible and not done,
because the ACTIVE segment (where appends land, and where figaro's hot reads
are) needs the lock either way.

**2. The budget bounds a BUSY process and does nothing for a quiet one.**
Gluck: "eviction definitely should happen when a figaro has been idle for a
while." It does now. `segment.SweepIdle(keep)` drops every block unread for
`keep` sweeps; `Angelus.evictIdleArias` calls it with keep=2, **riding the
reclamation sweep that already exists rather than introducing a fourth idle
clock**. The window is therefore `dormant_after / sweep_interval`, in the
same family as the three around it.

For the record, those clocks and their rates, since they are ordered oddly:

| what | knob | default |
|---|---|---|
| an aria's agent and its decoded caches | `dormant_after_minutes` | 15 min |
| figwal's lineage heads (and their blocks) | `handle_idle_minutes` | 5 min |
| a form's writer goroutine | `actor_linger_ms` | 2 s |
| segment payload blocks | rides the sweep, keep=2 | ~2 sweeps |

figwal unloads a head at 5 minutes while the agent above it lives to 15, so a
quiet aria drops its RAW bytes and keeps its DECODED ones. That predates this
work and is worth fixing as one policy rather than four.

### The idle sweep, proved on a live daemon (and a first attempt that proved nothing)

`/var/tmp/figstate/sweeplive.sh`. First run: a full listing filled the cache,
and five seconds later it was empty — but `loaded-heads` had gone 316 → 0, so
**figwal's head unload had done it, not my sweep.** A green light for the
wrong mechanism.

Second run pins the heads open (`handle_idle_minutes = -1`) so nothing else
can free a block:

```
right after a full listing   loaded-heads=317  segment-cache=21.9 MiB  loads=801
+5s idle                     loaded-heads=317  segment-cache=15.0 KiB  loads=801
+10s idle                    loaded-heads=317  segment-cache=0 B       loads=801
and a listing still works afterwards
```

Heads pinned, blocks gone, reads fine, and `loads` unchanged at 801 — so
nothing reloaded behind the sweep. That is the wiring proved end to end,
which is the standard this project set after shipping two knobs that were
configured and unwired.

**A flaw in my own harness, found while doing it**: my live scripts ran
`figaro serve`, which is not a command. The daemon is auto-started by the
first CLI call, so every measurement stands — but the `grep` of `daemon.log`
in those scripts was reading an empty file and could never have failed. Both
scripts are corrected. **A check that cannot fail is worse than no check**,
because it is counted as evidence.

### The third claim dies: it does NOT pay in file descriptors

I wrote "it pays in TIME and in file descriptors" into a commit message.
Half of that is wrong, and `/var/tmp/figstate/fdcount.sh` says so — the same
listing, on both trees, with heads pinned open (`handle_idle_minutes = -1`)
so nothing is released underneath the measurement:

| 318 heads loaded | base | after |
|---|---|---|
| open file descriptors | 1227 | **1219** |
| of which segment files | 1218 | **1210** |
| heap alloc | 200.0 MiB | **79.8 MiB** |

**Identical**, and the reason is obvious in hindsight: 1210 fds over 318
heads is 3.8 per head, which is one ACTIVE segment per channel — and the
active segment is exactly the one lazy opening still opens eagerly. Most
nodes in this store have one or two segments per channel, so there is almost
no sealed segment to defer.

So the ledger for lazy segment opening, honestly:

- **Dead**: it cuts memory (48.7 → 48.6 MiB), it cuts the 2.7 s first listing
  (unmoved), it cuts file descriptors (1227 → 1219).
- **Alive**: opening a node is 28x cheaper in the microbenchmark (5.42 ms →
  0.19 ms on a 32-segment log), and that reaches the store where something
  opens a node and reads little of it — `RangePatches` opens one per cold
  read (11.4 → 10.5 µs). End to end on the real store it buys 8–23% of wall
  clock:

| phase | base | after |
|---|---|---|
| topology + labels | 344 ms | **302 ms** |
| touching every board (368 more heads) | 401 ms | **310 ms** |
| visiting every aria | 4.079 s | **3.751 s** |

**The memory is the payload cache's doing, not lazy opening's**, and the
right way to read this session is: one change that mattered enormously
(payloads), one that is correct, cheap and modest (opening). Three of my
claims for the second one died on measurement. I am leaving it in because it
is the right shape and it is proven correct, not because it was worth what I
said it was.

## An idle daemon gives its arena back (`77b9fcb7`)

Not on any phase list; it comes from measuring Gluck's live box while the
rest of this was running. **The "2 GB" is a FLEET**: 1731 MB of PSS across 33
figaro processes — one daemon at 536 MB, a second at 129 MB, thirteen more
between 17 and 59 MB, and seven CLIs at ~25 MB each. `GOMEMLIMIT` is per
process, so it never bit; the aggregate has no ceiling at all.

Go's collector returns spans lazily and only under pressure, so a daemon that
has finished a burst keeps the arena it grew for it. Once a sweep finds
nothing live and nothing resident, twice running, the daemon now calls
`debug.FreeOSMemory`.

`/var/tmp/figstate/idlemem.sh`, same script both trees, real-store copy:

| | after a listing | +5 s idle | +40 s idle | after listing again |
|---|---|---|---|---|
| base | PSS 251 MB | 251 MB | 259 MB | 272 MB |
| after | PSS 141 MB | **51 MB** | 59 MB | 62 MB |

**The base never gives anything back — it only grows.** Five times, on the
state most of those thirty-three processes spend their lives in.

The creep from 51 to 59 MB across the idle window is the measurement itself:
each `doctor mem` poll allocates ~1.1 MiB and the latch has already fired.

**Once per quiet period, not per sweep**: `FreeOSMemory` is a stop-the-world
collection plus a scavenge — pointless to repeat and rude to run while
anyone is working. Any work resets the latch, and the latch is what the test
holds, because the release itself is not observable but "fires once, resets
on work" is.

### The three memory layers now, and which knob owns each

| layer | bound by | reclaimed when |
|---|---|---|
| figaro's decoded IR / translations / patches | `ir_window_mb`, `translation_window_mb`, `form_patch_window` | `dormant_after_minutes` sweep |
| figwal's raw segment payloads | `segment_cache_mb` | budget pressure, the sweep (keep=2), or a head unload |
| the Go arena underneath both | `soft_limit_mb` (GOMEMLIMIT) | **two quiet sweeps, now** |

The third had no reclamation at all before this, which is why the first two
kept being measured against a process that never shrank.

### Validation gate, session 3

```
figwal:  go test ./log ./disk ./segment ./xwal -race -count=3   ok (x3 releases)
         crashtest short + `-long -seed=11` (113 s)             ok
figaro:  build, vet, full suite -count=1                        ok
         -race -count=3 on store, actor, figaro                 ok
         nix build .#default (vendorHash reset x4)              ok
         fleet 12 arias x study x 300 patches                   12/12, four runs
         live daemon on a copy of the real store                ok
         idle sweep proved with heads pinned open               ok
```

Fleet, final build, against the four columns:

| | before WAL | after WAL | end s1 | end s2 | **end s3** |
|---|---|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 | 12/12 | **12/12** |
| history build | 4.11 s | 4.98 s | 4.99 s | 5.14 s | **4.96 s** |
| turn wall | 4.53 s | 5.49 s | 5.01 s | 5.85 s | **4.53 s** |
| control | 0.17 s | 0.16 s | — | 0.16 s | **0.16 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | 46.5 M | 48.3 M | **47.4 M** |
| goroutines | 93 | 80 | 80 | 80 | **80** |
| heap_alloc | 14.9 M | 16.0 M | 9.2 M | 10.5 M | 13.8 M |

**A fleet run of mine was polluted and I nearly published it**: history build
read 8.49 s because I had started a `nix build` in the same minute. Re-run
alone it is 4.96 s. The control catching it (0.18 vs 0.16 s) is exactly why
the harness has one.

## The cold-open fold is dead, and this time the reason is structural

It has been on the queue since session 2: *"revisit the cold-open fold, which
becomes a real saving the moment a record read can miss."* Reads can miss
now, so I measured it before building anything
(`internal/store/foldcheck_test.go`, `FIGARO_PROBE_FOLD=1`):

| form history | cold replay (figaro) | figwal `StateAt` fold |
|---|---|---|
| 500 patches | **1.99 ms** | 43.0 ms |
| 5000 patches | **18.9 ms** | 449.7 ms |

**The fold is 20 to 25 times SLOWER**, and the reason is not the one either
of us guessed:

1. **A reducible segment's header holds the fold at the segment's START.** So
   `StateAt(last)` folds that header with every payload from the segment base
   up to `last` — and figaro's segments are 2 MiB, which holds roughly twenty
   thousand form patches. **A real form is ONE segment**, so there is no
   earlier watermark to start from and the fold does exactly the work the
   replay does.
2. On top of that it pays an fsync (`log.StateAt` syncs first, because the
   disk fold must see every appended patch) and a JSON round trip through the
   reducer per folded record, where figaro's replay applies patches in
   memory.

So the fold can only pay when a form spans MANY segments, which would need
either much smaller segments for form channels or boards far longer than any
that exist. **Closed, with the measurement, rather than left on the queue for
a fourth session.** If it is ever reopened, the lever is segment SIZE for the
form channel, not the fold.

That also retires the last of the three "figaro-side open" ideas: my
predecessor's version-addressed open (+338%), the fold from the reducer
(+2000%), and my own lazy segment opening, which was correct and modest. The
cold form open is 19 ms for 5000 patches because it decodes 5000 JSON
patches, and the only thing that changes that is decoding fewer of them.

## Phase 7 (retention): DEFERRED, and this is the decision, not a gap note

The queue put retention next. I am not building it, and the reasoning is
here rather than in a docs footnote, because the last time I recorded a
judgement quietly Gluck had to ask.

**What it would be.** Retention is not a compaction pass: it is "how many
sealed segments a channel keeps" (§10). Every reducible channel already rolls
segments and a reducible segment's header is the folded state at its start,
so the fold a compacted channel needs is written by the ordinary roll. The
bottom of it exists already — `disk.Log.TruncateFront` drops whole sealed
segments, closes them (which now releases their payload blocks), and fsyncs
the directory. Enforcement at ROTATION would be safe by construction: a log
with child forks is read-only and never rotates, so retention can never eat a
prefix a sibling reads through.

**Why it is not worth building today, in order:**

1. **Its only customer is the topology form, and that customer does not need
   it.** figaro's segments are 2 MiB and a promote record is ~100 bytes, so a
   topology form rolls its first segment after roughly TWENTY THOUSAND
   promotes. The growth my predecessor flagged as "correct, and unbounded" is
   unbounded at a rate of one segment per twenty thousand promotes.
2. **figaro cannot express it per form anyway.** xwal channel options are
   per-channel-NAME, store-wide: `chanForm` is every board in the store, and
   boards hand out `PatchesBetween` VIEWS, which §10's own rule forbids
   retaining. Giving the topology form a policy needs either its own channel
   name (every node then carries an empty directory for it, and the form that
   exists on this branch needs migrating) or a per-node option override in
   xwal. Both are real changes serving item 1.
3. **A knob nobody sends is the thing this project has twice refused.**
   Session 2 declined `session`/`seq` on exactly that argument.

**When it becomes worth it**: when librettos hold LT RANGES into their source
instead of a copy (§12.3's note), retention on a source form can be driven by
the ranges its librettos still name. That is the real customer, it arrives
with phase 9's successor, and it will want the per-form expression problem
solved anyway.

**What I would build first when it is time**: `disk.Options.KeepSegments`
enforced in `rotateLocked`, `ChannelSpec.KeepSegments` to carry it, and the
type-level rule that a form with a retention policy refuses
`PatchesBetween` — that last one is the only part that is design rather
than plumbing, and it is the part that keeps a compacted channel from
silently handing out a view of records it has deleted.

## Phase 9, first half: the libretto exists (`ac3314bc`)

The derived form itself, with its fold and its refcount, in
`internal/store/libretto.go`. Deliberately stops at the boundary where
another aria's files begin.

**Shape**, per the rulings and §12: one per studied FORM, shared, named
`@libretto::<formid>` (deterministic, so nothing is looked up), on a reserved
stump (the only node figwal names by a caller-chosen string), does not fork,
holds a COPY.

**The three decisions worth arguing with:**

1. **The mirror applies the source's patch VERBATIM.** Whole-form is the
   ruling, so the fold is not a projection: the same `message.Patch` goes
   into the libretto's own form, which means the copy shares the source's
   immutable value nodes instead of marshalling through JSON. That is §12.7's
   tree surgery, obtained for free by not doing anything clever.
2. **Bookkeeping lives under `system.libretto.*`.** A verbatim mirror copies
   arbitrary board keys, and a board may legitimately hold a key called
   `refs`. `system.*` is the one namespace an ordinary write cannot touch, so
   the collision is impossible rather than unlikely, and the mirror skips any
   key in it. `TestLibrettoNeverMirrorsItsOwnBookkeeping` is the guard, and
   it writes the collision PRIVILEGED, because that is the only way to create
   it at all.
3. **A resync re-seeds rather than resumes.** If the subscriber falls behind,
   the answer is the source's current state, not the next patch: a mirror
   that skips one patch is wrong forever and does not know it.

**Two things it needed, both worth having anyway**: `ErrFormMoved` (the
stale-`ifVersion` refusal was a bare `fmt.Errorf`, so no caller could tell
"retry" from "wrong"), and `Subscription.Source` (a subscriber told to resync
must re-read the form it follows, and carrying that pointer separately is how
mirrors resync from the wrong one).

**What is NOT built, and it is the wiring rather than the mechanism**: the
study verb's two-participant write (§12.2.1), fork/import/kill as refcount
participants (§12.2.2 — read it first, it is a design bug found before the
thing was built), the IR's per-libretto cursors (§12.5), the reconciliation
sweep, and reclamation of a libretto whose refs hit zero. The first three
live in `internal/figaro/study.go`, `internal/angelus/study_hub.go` and
`internal/provider/projection.go`, which aria 6c2d7b9f owns.

**Phase 9 should also be checked against two bugs it is expected to fix**:
the self-cast deadlock, and the displaced-`tool_result` corruption — study
becoming an ordinary patch on a separate node is what removes the
out-of-band IR record between a `tool_use` and its result.

### The reconciliation sweep (`5f4081fa`)

`XwalBackend.ReconcileLibrettos` recomputes every libretto's refcount from
the boards that name it, and reports what it found rather than only what it
changed: boards read, librettos examined, **corrected**, **orphaned** (a
libretto no board names — the state reclamation acts on) and **missing** (a
studied form with no libretto, reported and never created, because minting
one is the study verb's job and only it has the source to seed from).

Recompute, never adjust. That is the whole design point: the study path's
ordering promises only that a crash leaves the count too HIGH, and §12.2.2's
three sites (fork, import, kill) break it the other way. A sweep that
adjusted would repair the recoverable direction and not the other one.

**A duplicated constant, and its guard.** The store needs `system.studies` to
read a board's study set, and it cannot import `internal/figaro` because
figaro imports the store. So the name is declared twice, and
`studies_key_test.go` (an EXTERNAL test package, which may import both)
asserts they agree. Without it, a rename on one side leaves the sweep reading
nothing and reporting every count as zero — a sweep that has gone blind while
producing correct-looking output, which is the worst failure a repair tool
has.

### A constraint the libretto exposes: refs==0 is NOT enough to reclaim

Found while writing `Reclaimable`, and it belongs in durable-forms §12
whenever someone next edits it.

The refcount answers "is anyone STUDYING this now". Reclamation needs more
than that, because **an IR record stamps the libretto version it was rendered
against** (§12.5). An aria that studied a form in the past and dropped it
still references that libretto for the whole of its history: unlink on
refs==0 and those records become unrenderable — which is precisely the
coupling the COPY exists to remove (§12.3: "deriving a pointer is not
derivation"), reintroduced from the other end.

So reclaiming a libretto needs a second question — *does any surviving IR
reference it* — and that question has no answer in the store today. Three
ways it could get one, none built:

1. **Delete-path only**: a libretto is reclaimed when every aria that ever
   stamped it is gone. Sound, needs no new state, and reclaims almost never.
2. **A reverse index**: arias-that-stamped-this-libretto, maintained on the
   libretto. New durable state with its own drift problem, and the
   reconciliation sweep would have to recompute it too.
3. **Let the render degrade**: absence is the truthful default (§1), so a
   missing libretto renders as "no study state for this record" rather than
   as an error. Cheapest, and it is a decision about what an old transcript
   is allowed to lose — Gluck's, not mine.

`Reclaimable` says all of this at the method, because the name promises more
than the number can deliver.

**A flake to know about, ownership unproven.** One `go test ./...` run failed
`TestFuzzFormUnkeyed/SetWhileTurnInFlight` with *timeout waiting for
turn.done*. It passes at `-count=5` in this tree and `-count=8` in the merge
base, and four other full-suite runs of this build were green, so it is
load-sensitive rather than new — but I could not reproduce it on the base
either, so "not mine" is inference, not proof. If it recurs, the suspect is
the gate/park timing in that fuzz case under a loaded box, not the form path.

---

## THE NEXT WORKER'S FIRST JOB (superseded — see "WHAT IS ACTUALLY LEFT" at the end)

## phase 9's second half, the wiring (mostly done since this was written)

The libretto MECHANISM exists and is tested (`internal/store/libretto.go`,
`libretto_reconcile.go`, `doctor librettos`). What is missing is every place
it has to be driven from, and most of it lives in files aria 6c2d7b9f owns —
**ask before touching, and check whether that fork is still alive; its last
message said "this fork is done"**.

In the order I would take them:

1. **The study verb's two-participant write** (§12.2.1), in
   `internal/figaro/study.go` and `internal/angelus/study_hub.go`. Libretto
   first (mint if absent, seed from the source, `Retain`), board second
   (`system.studies` gains the id). Drop is the inverse order. Every crash
   then over-counts, which the sweep repairs; the reverse cannot be repaired.
   **This is also the change that should fix the self-cast deadlock and the
   displaced-`tool_result` corruption**, because study stops being an
   out-of-band IR record — check it against both.
2. **Fork, import and kill as refcount participants** (§12.2.2). Read that
   section first: it is a design bug found before the thing was built. Fork
   must `Retain` every libretto the parent's `study-set` names BEFORE the
   child exists; kill must `Release` them.
3. **The IR's per-libretto cursors** (§12.5), in
   `internal/provider/projection.go`: N cursors, one per studied form,
   pointing at LIBRETTO versions rather than source versions. The translator
   then reads `PatchesBetween` on the libretto and never touches a source
   form, which is the point of the copy.
4. **Reclamation**, which needs a decision before it needs code: `refs == 0`
   is NOT sufficient, because an IR record references a libretto forever. See
   "A constraint the libretto exposes" — three options, all cheap, and the
   choice is about what an old transcript may lose.

### Then, in order

- **Phase 10, the API refactor** and `angelus.hello`. The lock audit's first
  fast-follow (`figaro/agent.go`'s `mu`, an aria's own state guarded by a
  lock beside an inbox that exists to own it) belongs with it, and the audit
  says it wants its own branch and its own pty runs.
- **Phase 7, retention** — deferred with reasons; see its section. Its real
  customer is librettos-holding-LT-ranges, not the topology form.
- **The four idle clocks**, which are ordered oddly: figwal unloads a head at
  5 minutes while the agent above it lives to 15, so a quiet aria drops its
  RAW bytes and keeps its DECODED ones. One policy, not four.

### Clean up after the live scripts

Every one of them copies the real store (281 MB, reflinked but not free) into
`/var/tmp`, and a daemon started against it holds it. By the end of this
session mine had accumulated **6.6 GB**. `rm -rf /var/tmp/figlazy.* figsweep.*
figfd.* figidle.* figstudy.*` when a run is done; the scripts print their
ROOT for exactly this reason, and none of them cleans up on its own because
a failed run's store is usually the thing you want to look at.

### What I would measure again before believing anything

```
FIGARO_PROBE_ROOT=<copy> go test ./internal/store -run DaemonDay -v   # the memory picture
FIGARO_PROBE_ROOT=<copy> go test ./internal/store -run ListingCost -v # one listing
scripts/live/idlemem.sh      # PSS on an idle daemon, before and after
scripts/live/sweeplive.sh    # the segment cache's idle sweep, heads pinned
scripts/live/studylive.sh    # study, fork, drop, doctor librettos
scripts/live/realstudy.sh    # the same against a copy of the REAL store
scripts/ariastress.sh --arias 12 --study --study-patches 300          # the fleet
```

**The live scripts now live in the repo** (`scripts/live/`, with a README
naming what each one caught). They were in `/var/tmp` until the end of this
session, which is where they would have been lost.

### Traps, added to the four I inherited

5. **`-benchtime 100x` measures a one-time cost.** A cold-read benchmark read
   +475% because a segment load moved out of untimed setup into the first
   measured iteration. At 4000x the same change is +18%.
6. **Do not run two heavy jobs at once.** A fleet run read 8.49 s against a
   5.0 s baseline because a `nix build` was in the same minute. The control
   column is what catches it.
7. **A check that cannot fail is worse than no check.** My live scripts ran
   `figaro serve`, which is not a command; the daemon auto-starts, so every
   measurement stood, but the `grep` of `daemon.log` beside them was reading
   an empty file and counted as evidence.
8. **Prove the mechanism you think you are proving.** The first idle-sweep
   run showed the cache emptying — via figwal's head unload, not my sweep.
   Pinning heads open (`handle_idle_minutes = -1`) is what made it a test.

## The boundary opened, and the live defect behind it (`d6b97f6e`)

Aria 6c2d7b9f answered: `study.go`, `study_hub.go` and `projection.go` are
**free**, PR 16 is merged, it holds nothing. It handed over three things with
them, and the first is a defect with a body count.

### The study mark could land inside a tool round, and did

`appendStudyMark` wrote an IR record from the RPC goroutine with no regard
for whether the drain loop was mid-round with an open call. From a real
aria's IR (`arias/ir/n714`):

```
127 input   tool_result  toolu_01ByJoaN…
128 output  tool_invoke  toolu_01Tqv3GS…   <- the call
129 input   content:null study:{began:true} <- the mark
130 input   tool_result  toolu_01Tqv3GS…   <- its result, one record late
```

A study mark is contentless and still encodes to a user message carrying a
system-reminder, so it DISPLACES the result, and every provider refuses that:
*tool_use ids were found without tool_result blocks*. **Two arias in this
lineage were bricked by it.**

**Fixed structurally, at the write site.** The mark is an inbox event now; the
loop writes it — immediately when idle, at the ROUND BOUNDARY when a turn is
in flight, which is where a queued `set` and a steering prompt already land
and where every tool_result of the finished round is already appended. The
record cannot land inside a round because nothing but the loop writes it.

**Not a repair.** Synthesizing a `tool_result` per dangling id was the fix in
these notes two sessions ago and it is wrong twice: two results behind one
call is also refused, and it lies about a call that succeeded.

**THE RULE for everything phase 9 adds next: no out-of-band IR record between
a `tool_use` and its results.** Every cursor the libretto wants to stamp
inherits it.

`TestStudyMarkCannotLandInsideARound` parks a round, asserts the mark stays
out of the IR for 300 ms (long enough that the old synchronous append would
have landed), then asserts it arrives after the boundary — and that the
DECLARATION landed immediately, because the board is the mechanism and the
mark is only narration. **Verified red against the old code.**

### Two more warnings from 6c2d7b9f, for whoever takes the projection

1. **`projection.go` has a seam that eats new fields.** Per-libretto cursors
   mean new fields on `IncrementalProjection`, and all four providers rebuild
   that struct BY HAND after a live append (`anthropic.go`, `anthropicsdk.go`,
   `copilot/responses.go`, `openaichat.go`). A field forgotten there is not
   lost state, it is lost POSITION: the next pass believes the cursor sits at
   zero and refolds the whole history to catch up — correctly, every turn,
   forever. It ate `LastStudyVersions` once and `FormVersionOfSnapshot` again
   last night. `TestSplicePreservesTheBoardPosition` guards it both ways;
   EXTEND it rather than trusting yourself.
2. **The cursor advance sits ABOVE the `acc == nil` branch on purpose**, so a
   dead form advances identically whether its record was cached or encoded.
   Keep that when adding per-libretto cursors: the hit and miss paths must
   agree, or the per-LT cache makes whichever ran first permanent.

Their branch `fix/tool-result-adjacency` exists and is EMPTY; the work landed
here instead, so it can be deleted.

## The verb mints its libretto now, on both halves (`2e4490b3`)

Study and drop move the refcount in §12.2.1's order, on the agent's loop for a
live aria and on the hub for a dormant one — the two halves that have had to
agree about the study set since `set` was served from the hub.

```
study: libretto retained FIRST, board declares SECOND
drop:  board stops claiming it FIRST, libretto released SECOND
```

**A study that changes nothing hands its reference straight back**, so a
repeated study is not a second reference: the board is a SET and the count is
derived from it. Both tests end by running the reconciliation sweep and
asserting it corrects **nothing** — that is the invariant the verb and the
sweep exist to share, and it is a better assertion than either half alone.

**Best-effort, through an optional interface.** An ephemeral backend has no
librettos, and a libretto that cannot be reached must not block a
declaration, because the board is the authoritative fact and the sweep
recomputes from it.

**The sigil bug, for the next person who meets it**: an unbound form is
addressed `@abc123` and its libretto's STUMP is named for the bare id. So the
name is stripped and the LOOKUP is not; getting it backwards produces
`xwal: unknown trunk "abc123"`, which is exactly how it was found.

### Where phase 9 now stands

| piece | state |
|---|---|
| the libretto: form, fold, refcount, death record | **done** (`ac3314bc`) |
| reconciliation sweep + `doctor librettos` | **done** (`5f4081fa`, `0bd1a7fa`) |
| store-side `StudyForm`/`DropForm` (ordering) | **done** (`0c5a6353`) |
| the study mark cannot land inside a round | **done** (`d6b97f6e`) |
| the verb retains/releases, live and dormant | **done** (`2e4490b3`) |
| fork/kill as refcount participants (§12.2.2) | **done** (`f7c39f69`); import does not exist as a verb, and inherits the rule when it does |
| **the IR's per-libretto cursors (§12.5)** | **not started** |
| **the translator reading librettos instead of sources** | **not started** |
| **reclamation** | blocked on a ruling: refs==0 is not sufficient |

The next two are the ones that change what a user sees, and both live in
`internal/provider/projection.go` and the four provider encoders — read
6c2d7b9f's two warnings above before touching either.

### §12.2.2 closed, and the flake explained

Fork retains before the child exists; kill releases before the tombstone,
because a sealed form cannot be read back for its study set. The fork test is
verified red without the participant (*refs after a fork = 1, want 2*), and
both end by running the sweep and asserting it corrects nothing.

**The `SetWhileTurnInFlight` flake is understood and fixed.** It timed out on
two different builds under a full `go test ./...` and passed at `-count=5`
and `-count=8` in isolation. The cause is not timing luck: **the WAL change
made every queued `set` a mandatory fsync**, and that test drains 160 of them
inside one turn — half a second on an idle box, several times that when a
dozen packages compete for one disk. `fuzzTurnTimeout` was 5 s, chosen before
durability was mandatory, and its own comment calls it a liveness guard
rather than synchronisation. Raised to 20 s, with the arithmetic in the
comment. **A test whose deadline is a measurement of the disk is a test that
will lie eventually.**

### Two bugs the live run found, and the lesson under them (`38b2b308`)

`/var/tmp/figstate/studylive.sh` drives the real verbs on a real daemon:
`form new`, `study`, `ls -g`, `fork`, `doctor librettos`, `drop`. It found
two things the whole unit suite had missed.

1. **`fig ls -g` drew the libretto stump.** `listStumps` filtered the
   topology form BY NAME and nothing else, so the first study minted a row
   describing machinery. Reserved stumps are filtered as a CLASS now, using
   the predicate the sweep already had.
2. **The sweep disagreed with the verbs: `would correct 1`.** A live fork
   takes `ForkWith`; I had put the participant on `Fork` and `ForkAt`. Every
   unit test passed because every unit test called `Fork` — **the CLI calls
   the one I missed**. And under-counting is the unrecoverable direction, so
   this was §12.2.2's exact failure reintroduced by an incomplete fix to
   §12.2.2.

**The lesson, and it is the one worth carrying**: a test that exercises the
entry point the TEST chose proves nothing about the entry point the PRODUCT
uses. The fork test now runs over both in a table, and the listing assertion
walks `Nodes()` and `Forms()` rather than trusting one filter. Ninety seconds
of live driving found what a green suite could not.

## The projection switch: CORRECTED by Gluck (see durable-forms 12.5b), then analysed below

**Read this first**: the analysis below talks itself into a permanent dual
rendering path for legacy stamps. That is wrong. Gluck: figaro stamps the
LIBRETTO's cursor and the translator reads the libretto; records already on
disk carrying SOURCE cursors are simply IGNORED (their study block is absent,
which the reclamation ruling already licensed). A namespace and a skip, not
two rendering paths. And a libretto is fully persistent — no retention, no
compaction, no dropped segments, ever — because the translator asks it for
arbitrary historical ranges and a dropped segment answers the wrong one
silently.

## (the original analysis, kept for its trap, which is still real)

§12.5 wants the IR to stamp LIBRETTO versions and the translator to read
librettos instead of source forms. I traced both ends before touching
anything, and the good news is that **it does not need `projection.go` or the
four encoders at all**:

- the translator's accessors come from `Agent.studyAccessors`
  (`internal/figaro/agent.go`), which builds `formView{id: fid}` per studied
  form. Pointing those at `LibrettoID(fid)` is a one-line change.
- the stamps come from `XwalStore.observedCursors`, which reads
  `formTail(fid)`. A libretto is a stump with a form channel, so
  `formTail(LibrettoID(fid))` works unchanged.

So no new field on `IncrementalProjection`, and 6c2d7b9f's seam is not
touched. **But do not just do it**, because of this:

### THE STAMPS ARE DURABLE HISTORY

Every IR record ever written by a studying aria carries `StudyVersions`
keyed by form id, holding **source-form versions**. Switch the
interpretation and every one of those numbers is read against the wrong log
on the next retranslate: a libretto's version 7 is not the source's version
7, and the ranges will be silently wrong rather than absent. The per-LT cache
then makes whichever rendering ran first permanent.

**So the switch needs a second cursor namespace, not a reinterpretation.**
`studyCursorPrefix` already namespaces these in the record's cursor map; a
`libretto:` prefix beside it lets old records keep their meaning and new ones
carry the new one. The projection reads whichever it finds — legacy stamps
through the source accessor (exactly today's behaviour, including the
"removed while studied" note), new stamps through the libretto. That is more
code than the one-liner, and it is the difference between a migration and a
silent corruption of every existing transcript.

**Whoever takes it should also ask whether it is worth doing at all yet.**
What the switch buys is §12.3's three properties — the translator never
touches a source form, source forms become freely deletable, and the render's
special cases become ordinary state. What it costs is a permanent dual path
in the projection. My instinct is that it is worth it and that the dual path
should be written as "legacy stamps are read from the source, and there is a
dated comment saying when that can be deleted", but it is a judgement about
history compatibility and it should be Gluck's.


---

# WHAT IS ACTUALLY LEFT (written last; this supersedes every earlier list)

Phase 9's mechanism, its verbs, its migration and its safety net are done and
live-validated on a copy of the real store. Both bugs the phase was supposed
to close are closed: the displaced `tool_result` (the study mark now rides
the inbox) and the self-cast deadlock (the cast now does not).

What remains, in the order I would take it:

**~~The projection switch~~ is DONE** (session 4, `e0fd5c34`): the stamps,
the accessor and the machinery filter. Phase 9 is complete. What remains:

1. **A form REBORN under the same id is still silently discarded**
   (`wym.md:22`). The other half of that paragraph — a dead source is never
   unsubscribed — is now built (`b60d1fd6`), and it is the enabler for this
   one: `Following()` goes false on death, so `b.libretto()` will re-attach
   on the next verb. What still breaks is the watermark: a reborn form's
   channel restarts at 1, and both the seed guard (`libretto.go`, "seed only
   when the copy is BEHIND") and the event guard (`ev.Version <= l.At()`)
   discard everything below the DEAD form's high water mark. `alive` also
   stays false.

   **The shape of the fix**: rebirth is detectable — the source's current
   version is BELOW our `at` — and the answer is to re-seed from the new
   source and reset `at` and `alive` together, as one patch. The trap is that
   version numbers alone cannot distinguish "reborn" from "a stale read", so
   whoever builds it should tie the reset to the node identity figwal already
   has rather than to the number.
2. **Show him the two-participant write.** He approved it *conditionally on
   seeing the code* (`answers-forms.md:12`) and the review was never asked
   for. Five minutes of his time, and the retry count has since gone 5 → 32.
3. **Strike the stale design text**: §12.3 still carries `"paths"` in the
   libretto document and §17 still lists the union projection as open
   `[q13]`, both settled as whole-form-only in `answers-forms.md:1`.
4. **Reclamation** — DEFERRED by ruling (§12.7b), not a gap. 3.0 KB.
5. **Phase 10, the API refactor**, and with it the lock audit's first
   fast-follow (`figaro/agent.go`'s `mu`). The audit says it wants its own
   branch and its own pty runs; believe it.
6. **Phase 7, retention** — deferred with its argument written down. When it
   lands, **retention must refuse a libretto BY TYPE, not by convention**
   (§12.5b): the translator asks for arbitrary historical ranges, so a
   libretto that dropped segments answers a wrong range silently, and once a
   source is deleted the libretto is the only copy.
7. **One idle policy instead of four.** figwal unloads a head at 5 minutes
   while the agent above it lives to 15, so a quiet aria drops its RAW bytes
   and keeps its DECODED ones. The four clocks are tabulated above.

**The one thing I would do first if I were staying**: run
`scripts/live/studylive.sh`, `realstudy.sh` and `renderlive.sh` after any
change to the study path or the caches. Ninety seconds of driving the real
verbs found two bugs a green unit suite could not, and both were in the
direction that loses data. `renderlive.sh` is session 4's addition and covers
the half the others never did: what the MODEL is actually sent, asserted on
the wire dump rather than on what a model chose to say about it.

### The durability gate, run deliberately (it is the one that matters for figwal)

```
FIGARO_CRASH_TEST=1 TMPDIR=/var/tmp go test ./internal/store -run Acknowledged
  attempt 0: 116 acknowledged patches, all durable
  attempt 1: 180 acknowledged patches, all durable
  attempt 2: 225 acknowledged patches, all durable
  attempt 3: 138 acknowledged patches, all durable
```

A child process patches a form and prints every version the writer said
landed; the parent SIGKILLs it at a random moment and checks every
acknowledged version is on disk. **This is the gate that matters for this
session specifically**, because the figwal work changed how records are READ
BACK — payloads are no longer materialized at open, sealed segments are
opened on demand, and blocks are evicted underneath readers. If any of that
had made a record unreadable after a crash, this is where it would show, and
it does not.

figwal's own `crashtest -long` was run on the lazy-open release and again on
the final one (`e44a843`, seed 17).

### The libretto's cost, measured (`0852d0f0`)

§12.7's tree-surgery claim, checked against the built thing:

| source board | allocation per fold |
|---|---|
| 10 keys | 31 KB, 343 allocs |
| 5000 keys | **38 KB, 439 allocs** |

**Flat in board size.** Marshal-and-unmarshal would have made the second row
megabytes; the fold applies the source's own `message.Patch` to its own tree,
so the copy shares the source's immutable value nodes.

**And the cost nobody had named: the copy is DURABLE, so a studied form pays
a second fsync per patch.** Now measured and reduced where it can be:

| writers on the source | source patches per libretto record |
|---|---|
| 1 | 1.005 |
| 8 | **4.000** |

The fold drains whatever is queued and applies it as one patch. That does
nothing for a sequential writer — the source is itself fsync-bound, so events
arrive milliseconds apart with nothing to coalesce — and takes a QUARTER of
the records under group commit, which is what a busy form actually produces.
The mirror's contract is the STATE, not the number of records it took to
reach it.

**Left honest rather than optimised away**: a studied form under sequential
writes still costs 2x the fsyncs it did unstudied. That is the price of the
copy being durable, it is the same price the WAL work chose everywhere else,
and the alternative (a libretto that is a cache rather than a form) gives up
replayability. If it ever hurts, the lever is the fold's own batching window,
not the durability.

### The fleet exercises it now, and a number I nearly published wrong

`ariastress.sh --arias 12 --study --study-patches 300 --keep`, on the final
build: 12/12 answered, control 0.16 s, history build 5.03 s (baseline 5.14 s
— no regression), and the store now contains `@libretto::e1d28cfb`. Audited
offline afterwards:

```
boards read      25
librettos        1
would correct    0     <- the verbs and the sweep agree after a full fleet run
orphaned         0
missing          0
```

**The mistake, recorded because it is the interesting part.** The libretto
holds 29 records for 300 source patches, and I read that as ten-to-one
coalescing and started to write it down. It is nothing of the kind: the
harness applies its 300 patches SEQUENTIALLY and only then studies, so
almost nothing was ever folded. Those 29 records are the seed, the twelve
retains (one durable patch each, one per observer) and the drops.

Two things follow, and both are more useful than the number I imagined:

1. **A retain is a durable record.** Twelve observers of one form cost twelve
   libretto records at study time. Cheap, correct, and worth knowing before
   somebody studies from a hundred arias at once.
2. **The coalescing measurement stands on its own benchmark**
   (`BenchmarkLibrettoFoldBurstConcurrent`, 4.0 patches per record under
   group commit) and NOT on this fleet, which cannot produce the case at all.
   A number from a workload that cannot exercise the mechanism is not
   evidence for it.

### `doctor mem` reports what studying costs (`f8fb8416`)

```
librettos  open=1  observers=2  (one fold goroutine each)
```

Printed only when there are any. A fold is a goroutine and a subscription,
and ONE libretto is shared by every observer of that form — which is the
whole argument for one-per-form over one-per-figaro, and nothing could check
it until now. Live: two arias studying one form give `open=1 observers=2`,
and `observers=1` after one drops.

This project has twice shipped a resident structure nobody could see (the
translation cache, figwal's snapshots). The rule that came out of it — a new
resident structure arrives with its number in `doctor mem` — is now applied
to phase 9 as well.

### One implementation of the two-participant write (`d8a97bbe`)

I built `StudyForm`/`DropForm` in the store, then wired the hub with a
parallel copy — its own read-modify-write of the board, its own retain and
release. That left the store's pair with **no production caller**: dead code
with good tests, which is the "a knob nobody sends" antipattern I had
criticised phase 7 for four hours earlier. Worth recording as a habit to
watch: building the mechanism and then wiring past it is easy to do when the
wiring happens in a different file on a different day.

The hub delegates now. **Two implementations of a crash-ordering rule is one
too many**, and they had already drifted: the hub retried on a version
conflict and the store's version did not.

So the store's gained the retry, and with it a guard the hub had and mine
lacked — `system.studies` is a read-modify-write, and without a version check
two arias studying different forms at once overwrite each other's
declaration. `ApplyFormEffectPrivilegedIf` is that guard for a system-managed
key. **The retry must also hand the reference back before it loops**, or a
contended study leaks one per attempt: a leak that appears only under
contention, and then only as a count that will not come down.

Net 20 lines fewer, and the live path re-verified end to end.

### And the agent's half delegates too (`1becdf8e`)

Three read-modify-writes of `system.studies` (agent, hub, and the store's
own) became one, with the agent keeping the single part the hub does not
need: refreshing its in-memory board mirror after the durable write, because
an agent renders from a snapshot and a write it does not hear about is a
write the next turn will not see.

47 lines added, 64 removed. **A crash-ordering rule that three call sites had
to agree about by inspection now exists once.**

### The IR-writer audit, and the participant I had written off (`0d2758f5`)

The tool_result-adjacency rule (*no out-of-band IR record between a `tool_use`
and its results*) is only as good as the list of writers it covers, so I
walked every `Append` into an IR log:

| writer | verdict |
|---|---|
| `Agent.appendStudyMark` | **was the defect**; rides the inbox now |
| `handlers.markStudyForHub` | safe by construction: the hub path serves an aria with NO agent, so there is no round to land inside. Worth knowing it depends on the live/dormant dispatch staying correct |
| `handlers.import` | safe: the conversation is created one line above; nothing is in flight |
| provider encoders (4) | inside the round, by the loop, which is where they belong |
| `figaro/repair.go`, `turn_repair.go` | repair paths, not turn paths |

**And one line further down in the import handler, §12.2.2's third site.** I
had recorded that import "does not exist as a verb" here. It does:
`angelus.import` restores a board wholesale, and **`system.studies` is an
ORDINARY key**, so an exported board carries it. The import then creates a
board naming librettos nothing counted — the unrecoverable direction.
`RetainDeclaredStudies` (the hook fork already used, renamed for what it does
rather than who calls it) is now called there.

**Left for the next hand, deliberately**: `system.studies` should be
`KeySystemManaged`. A comment of mine already claimed it was, and was wrong —
a hand-written `fig set system.studies '["@abc"]'` can still declare a study
nothing counted. Protecting it has real blast radius (the ephemeral path
writes it unprivileged, and import would then need the privileged entry
point), which is exactly the shape of change that should not be slipped in at
the end of a session.

## HANDOFF GATE (session 3), all green on `1a2d5db6`

```
go build, go vet, go test ./... -count=1                       ok
-race -count=3 on store, figaro, angelus, actor, provider      ok
FIGARO_CRASH_TEST=1 (acknowledged patches survive SIGKILL)     ok
nix build .#default                                            ok
figwal: -race -count=3 on log/disk/segment/xwal                ok
figwal: crashtest -long, on the lazy-open AND final releases    ok
fleet: 12 arias, study, 300 patches                            12/12
live: studylive.sh, sweeplive.sh, lazylive.sh, idlemem.sh      ok
```

Fleet, final build, against every earlier column:

| | before WAL | after WAL | end s1 | end s2 | **end s3** |
|---|---|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 | 12/12 | **12/12** |
| history build | 4.11 s | 4.98 s | 4.99 s | 5.14 s | **4.90 s** |
| turn wall | 4.53 s | 5.49 s | 5.01 s | 5.85 s | 5.44 s |
| control | 0.17 s | 0.16 s | — | 0.16 s | **0.16 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | 46.5 M | 48.3 M | **49.5 M** |
| goroutines | 93 | 80 | 80 | 80 | 81 |
| heap_alloc | 14.9 M | 16.0 M | 9.2 M | 10.5 M | **10.5 M** |

The 81st goroutine is the libretto's fold: the harness studies a form, so
exactly one exists, and it is visible as `librettos open=1` in `doctor mem`.
History build is the fastest it has been since before the WAL, with a
studied form now costing a second durable write per patch — which is worth
saying twice, because it means the fold is not on the critical path.

## Succession (session 3)

- **Successor minted**: `b2b0c543`, briefed with the reading order, the eight
  traps, the released file boundary, and the two rulings that gate its first
  job. Its id is in `/var/tmp/figstate/.successor`. It has read the plans and
  is waiting; it has NOT moved the role.
- **The role is `@980dc16c`.** On handoff:
  `figaro state set --id @980dc16c target-aria b2b0c543` — a form patch the
  hub serves with no agent involved. A form patch is still the right way to
  move a role; the reason it was the ONLY way is gone as of `30dcd6ba`, which
  took the cast off the inbox. **Against a daemon running an older binary it
  still hangs** — which is most of the ones on this box until they restart.
- It arrived at the same two hazards from the notes alone (§12.5 is a
  migration; `system.studies` is unprotected and reachable from the CLI), and
  named a third question I had left implicit: **is the projection switch
  worth its permanent dual path at all yet?** That is the right question and
  it is Gluck's.
- **THE ROLE MOVED to `b2b0c543` at 2026-08-13 ~10:20**, on Gluck's word,
  with all three rulings recorded in durable-forms §12.5b/§12.7b.
- **d604c755 stays alive as a reference**, as its own predecessor did.
- **Gluck's standing instruction to the new holder**: study the EARLIEST
  material first — `durable-forms.md` whole, `answers-forms.md` and `wym.md`
  (his own words, verbatim), and session 1 of this file — and check that what
  has been BUILT matches what was originally described. Report divergences
  rather than assuming the newest note wins. Then hurry: little work remains,
  and he wants a testable branch, having been at this all night.

### The fifty-observer storm (`a524bec2`)

§12.7 names it as the case where the derivation's cost stops being
theoretical: *"50 × 12 MB per patch versus 50 × a few hundred bytes"*. The
answer is not a cheaper fold — it is **one libretto per studied FORM**, which
is the reversal Gluck made on 2026-08-12 from one-per-figaro. Measured per
source patch:

| observers | time | allocation | librettos |
|---|---|---|---|
| 1 | 5.9–6.5 ms | 29.8–30.0 KB | 1 |
| **50** | **5.5–5.7 ms** | **29.8–30.0 KB** | **1** |

Flat. Fifty figaros watching a form cost what one costs, and `doctor mem`
says `librettos open=1 observers=50` while it happens. That is the whole
argument for sharing, as a number rather than a paragraph.

### The bug I nearly shipped: eviction ORPHANS a subscriber (`11639443`)

Found by asking what the idle sweep does to a form a libretto is following.
It evicts it — and **eviction does not stop a subscriber, it orphans one**:
the `Form` leaves the registry, the next write constructs a NEW instance, and
the old one, which is the instance the subscriber holds, never hears again.

So the libretto stopped following its source and went on serving a stale
copy. No error, no log line, refcount healthy, `doctor mem` content. **The
exact failure this session kept finding in other people's code, committed by
me four hours earlier.**

The design already had the answer and I had not connected it: §7 says *the
lease registry IS the subscriber set*. Eviction respects that lease now
(`Form.Subscribed()`), and the other half is tested as well — a lease that
never expires is a leak with a justification, so dropping the study must make
the form evictable again on the very next sweep, and it does.

**The lesson for the successor**: every new long-lived reader of a Form is a
lease-holder, and the caches underneath it were written before any such
reader existed. When phase 9's projection reads librettos, or phase 10 hands
out streams, ask this question again — *what does the sweep do to the thing
I am holding* — because the answer defaults to "silently the wrong thing".

### I created the two-worktrees-one-branch hazard myself, in figwal

The HAZARD section at the top of this file is about a fork checking a branch
out in someone's worktree. I did the same thing to `/home/gluck/dev/figwal`:
cut all four figwal releases from `/var/tmp/figwal-lazy` with `master`
checked out in BOTH, so master advanced under the main worktree while its
files stayed at `4f9ce6a`.

**What that leaves is worse than a stale checkout.** `git status` there
showed fourteen STAGED changes — my new files as deletions, my edits as
reversions — because the index still described the old tree. Anyone
committing in that worktree would have reverted the entire figwal session in
one commit, with a clean-looking diff.

Repaired with `git reset --hard HEAD` after checking that every staged path
was one of mine and nothing was untracked. The build there is green and the
code is present.

**The rule, restated because I proved it applies to the person who wrote it
down**: one branch, one worktree. `git worktree add -f /var/tmp/<name>
<ref>` for anything you are cutting releases from, and check
`git worktree list` before you use `-f` on a branch somebody else has out —
including yourself in another directory.

### A study of a deleted form could not be dropped after a restart (`176c7efb`)

§12.2.2 says it plainly — *drop on a form that has since been deleted is
legal* — and it was, but only while the libretto happened to be cached in the
daemon. **Across a restart it failed**:

```
libretto @450dea2c: source: xwal: unknown trunk "@450dea2c"
```

and the study was then stuck on that board **forever**, because every attempt
to drop it took the same path.

The fault was one call that had no business needing the source: opening a
libretto tried to SUBSCRIBE, and a subscription needs a live form. The copy
outliving what it copied is the whole point of §12.3, so a missing source is
no longer an error — the libretto opens, reads and drops without one, and
only the FOLLOWING stops. Not latched either: the next caller retries the
attachment, so a source unreadable for a moment is followed again when it is
not.

**The pattern in the last three bugs is worth naming**, because it is the
same one three times: *the copy is supposed to be independent of its source,
and every place I touched quietly re-coupled them*. The mirror copied the
source's tombstone and sealed itself; the sweep evicted the source and
orphaned the fold; opening the copy required opening the source. Each was
found by asking what happens to the libretto when the source is gone, idle,
or dead — **which is the question to keep asking**, because the design says
they are independent and the code keeps assuming they are not.

## Phase 9 meets the REAL store, and finds the migration (`943df8f5`)

`/var/tmp/figstate/realstudy.sh` runs the verbs against a copy of the actual
store — 715 rows, real boards, a topology form that has to migrate:

```
first listing            715 rows in 3.05 s
study an existing aria   librettos open=1 observers=1
fork it                  librettos open=1 observers=2
drop                     librettos open=1 observers=1
the sweep, 711 boards     0.89 s   corrected 0   orphaned 0   MISSING 11
```

**`missing 11` is the finding.** Eleven arias in the real store already study
forms — all studied before librettos existed — and none would ever acquire
one, because the verb mints at study time. Under the projection switch those
studies would render nothing.

So the sweep MINTS what is missing; it already had the boards (the source of
truth) and the source forms (to seed from), and minting is exactly the repair
a recomputing pass should perform.

**And the cheap guard I added this morning had to go.** `HasLibrettos()`
skipped a store with none — which is *every store from before phase 9*, i.e.
precisely the ones needing migration. A guard that looks like thrift and
excludes the only case that matters is a bug. The pass runs in the background
at boot instead: 0.89 s on 711 boards, and no caller waits for it.

**The metric lied.** `Missing` counted the pre-state, so a pass that had just
minted four reported `missing 4`. It now counts what is still missing when
the pass ENDS, with `Minted` reporting the work done — same class of fault as
a check that cannot fail, caught the same way.

**And the idempotence earned its keep on the first real run**: the boot sweep
minted 7 of 11 before I stopped the daemon, and a later pass finished the
other 4. An interrupted migration resumes; it does not corrupt and it does
not restart.

### The repair command declined to repair (`7525ca87`)

`doctor librettos` had `if !dryRun && audit.Corrected > 0` — a shortcut
written before the pass could mint anything. So a store whose only problem
was a MISSING libretto was audited and left exactly as it was, which is the
entire migration case, declined by the command that exists to perform it.

Found by running the repair on the real store and then running it again:
**"corrected 0, missing 4" twice in a row is a repair tool that has given up
without saying so.** Now:

```
corrected 4   minted 4   missing 0
second pass:  would correct 0   would mint 0   12 librettos
```

**And a lesson I paid for twice today**: `./result/bin/figaro` is whatever
the last `nix build` produced, not what you just wrote. My first attempt at
this check used it and I read stale output as a real result — the giveaway
was wording I had changed an hour earlier. **Build to `/tmp` and test that**,
or you will debug the past.

## The self-cast deadlock, fixed and checked (`30dcd6ba`)

It has been in these notes since session 1: *`fig cast` on your own aria from
inside a turn hangs, because the cast rides the inbox and the inbox is
running the turn that issued it* — and "create a role as step one" asks for
exactly that. durable-forms says phase 9 should fix it **and should be
checked against it**. Checked first (red), then fixed.

It is the displaced `tool_result` seen from the other end: **one hangs
because it NEEDS the loop, one corrupted because it went AROUND the loop.**
Both are now closed, and by opposite moves — the study mark was pushed ONTO
the loop, the cast was taken OFF it.

**What the loop bought the cast was mutual exclusion between two castings of
one figaro, and phase 9 pays for that differently**: the study is a
version-guarded read-modify-write on the board, retried on conflict, and the
role's `target-aria` is a patch on the ROLE form's own single writer. Two
concurrent casts cannot lose each other's work, and two casts producing two
roles that both point here is what was asked for rather than a race. So a
cast runs on the caller's goroutine, and nothing waits on a loop that may be
waiting on it. `eventCast` and the reply channel go with it.

Live, on a real daemon: `cast` completes in **0.08 s**, the role points back
at the caster, the caster studies it, one libretto exists with one observer,
and the sweep corrects nothing.

### The migration is observable on a live daemon (`8b825a92`)

The boot sweep repairs in the background, which left one way to learn whether
it had done anything: stop the daemon and audit by hand. `doctor mem` now
carries the last sweep's result:

```
librettos  open=11  observers=13  (one fold goroutine each)
           boot sweep: minted=11 corrected=11 still-missing=0
```

Measured on a copy of the real store **four seconds after boot**: all eleven
pre-existing studies migrated, nothing left missing, no caller waiting.

`still-missing` is the number that matters on somebody else's machine — what
the sweep could NOT repair, which today means a studied form whose node is
gone from that store.

## CLOSING GATE (session 3, final), all green

```
go build, go vet, go test ./... -count=1                        ok
-race -count=3 on store, figaro, angelus, actor, provider       ok
-race -count=8 on the libretto/study/cast tests                 ok
FIGARO_CRASH_TEST=1 acknowledged patches survive SIGKILL        ok
nix build .#default                                             ok
fleet: 12 arias, study, 300 patches                             12/12
```

| | before WAL | after WAL | end s1 | end s2 | **end s3** |
|---|---|---|---|---|---|
| turns answered | 12/12 | 12/12 | 12/12 | 12/12 | **12/12** |
| history build | 4.11 s | 4.98 s | 4.99 s | 5.14 s | **4.89 s** |
| turn wall | 4.53 s | 5.49 s | 5.01 s | 5.85 s | **4.90 s** |
| control | 0.17 s | 0.16 s | — | 0.16 s | **0.16 s** |
| daemon PSS loaded | 56.8 M | 58.6 M | 46.5 M | 48.3 M | 51.8 M |
| goroutines | 93 | 80 | 80 | 80 | 81 |

**History build and turn wall are the best they have been since before the
WAL**, with mandatory durability everywhere, a second durable write per patch
on a studied form, and a libretto folding underneath. PSS is up 3.5 M on the
fleet against session 2 — the libretto, its fold and the boot sweep's
bookkeeping — and down 5 M against the pre-WAL baseline.

### What phase 9 costs on the real store, measured after the migration

The daemon-day probe again, this time on a copy that has been MIGRATED
(`doctor librettos`: minted 11, missing 0):

| phase | before phase 9 | with 11 librettos |
|---|---|---|
| topology | 48.6 MiB | **48.4** |
| listing | 48.7 MiB | **48.4** |
| touching every board | 68.4 MiB | **68.8** |
| visiting every aria | 320.9 MiB | **321.0** |
| after evicting idle | 49.2 MiB | **47.7** |

**Nothing measurable at rest** — within the noise of the probe. On disk the
eleven librettos are **3.0 KB in total** across 33 directories (one per
channel per stump), against a 262 MB store.

The cost that IS real is per LIVE fold: one goroutine and one subscription
per libretto actually being followed, plus a second durable write per patch
on a studied form (coalesced to a quarter of that under group commit). Both
are reported by `doctor mem`, and neither exists for a store nobody is
studying in.

### `system.studies` is protected, and import stopped copying it (`e81b93ab`)

The last CLI-reachable path into §12.2.2's unrecoverable direction, and the
stroke my successor had named as its first. Taken while it waited on rulings,
so it starts with more headroom.

```
$ figaro state set --id b1123524 system.studies '["@deadbeef"]'
error: system.studies: written by the harness, not by hand
```

**The blast radius was exactly where the note said**, and one test found it:
`import` applies a caller-supplied board patch UNPRIVILEGED, so protecting
the key breaks restoring a study set by copying it. The answer is NOT to make
import privileged — that would let an importer write `system.cwd` and the
model — it is to stop restoring studies by copying at all. **Import now lifts
`system.studies` out of the patch and replays each id through the VERB**,
which mints the libretto, seeds it and retains it. An import naming a form
this store does not have is fine: the libretto holds an empty copy and starts
following if that form ever arrives.

So import became a first-class participant rather than a board-copier, which
is what §12.2.2 asks for and better than the hook it replaces.

## FINAL GATE (superseded — see the one below, on `9d324c4f`)

## GATE on `27fe08bf`

```
go build, go vet, go test ./... -count=1                        ok
-race -count=3 on store, figaro, angelus (and -count=8 on the
  libretto/study/cast tests earlier)                            ok
FIGARO_CRASH_TEST=1 acknowledged patches survive SIGKILL        ok
nix build .#default                                             ok
fleet: 12 arias, study, 300 patches                             12/12
  history build 4.93 s   turn wall 4.98 s   control 0.16 s
  daemon PSS 49.2 M   goroutines 81
figwal: -race -count=3 on log/disk/segment/xwal, crashtest -long ok
live: every script in scripts/live/                             ok
```

**Session 3 totals**: 190 commits, 139 files, +17k/-1.2k against main
(including earlier sessions' work on the branch). figwal released four times
and pinned at `e44a843`, with the flake vendorHash reset each time and `nix
build` proven after each.

The one thing a reader should take away: **the memory this project has been
chasing was never figaro's.** It was one unbounded cache in the layer below,
and the layer below that never gave its arena back. Bounding the first and
returning the second took a store's whole history out of RAM — 297 MiB to
68.5 for a full pass over the real store — and every bound figaro had already
added kept working, on top of something that finally has one too.

### What a studied form costs while everyone sleeps (`b84b7c5c`)

Asserted as a test rather than left to be discovered later as a memory
question. After an idle sweep with a study outstanding:

```
observer resident = false      its own caches go, as always
source   resident = TRUE       a form with a subscriber is not idle (§7)
librettos         = 1          the fold is still live
```

So **a studied form is pinned for as long as any board names it**: one
resident `Form`, one fold goroutine, and a second durable write per source
patch — even when nobody is awake to read the copy. Bounded by the number of
studied forms (eleven on the author's store), reported by `doctor mem`, and
correct: the copy must be current when an observer wakes.

**The refinement nobody has needed yet**, written down so it is not
rediscovered: the fold could STOP while no observer is resident and catch up
with one seed patch on wake. That is legal — no IR record is stamped during
dormancy, so no stamp falls in the gap the catch-up would create — and it
would remove the double write for a form whose watchers are all asleep. It is
not built because nothing has measured it as a problem, and a knob nobody
sends is what this project keeps refusing.

### One writer per form — including the sweep's (`9d324c4f`)

The base rule of the design (§1: *one writer per form, an inbox with exactly
one drainer, not a mutex and not a convention*), broken by the reconciliation
sweep I wrote this morning. It opened its OWN `Libretto` per libretto
examined, so a copy already open and following had a **second `store.Form`
appending to its channel**, each computing versions from its own replayed
state:

```
the live libretto says refs=9 after the sweep corrected it to 1:
the sweep wrote through a second writer
```

`librettoInstance` is THE instance for a source now, and everything asks it:
the verb, the participants, the sweep. The sweep also stopped `Close`ing what
it examined — it was closing a `Form` another goroutine was folding into.

**And re-attaching no longer re-seeds.** `Follow` wrote the source's whole
state every time it attached, so re-attaching a current copy (after a
restart, or after a missing source came back) appended the whole board again.
It seeds only when the copy is BEHIND, which makes attachment free and keeps
a boot from adding a record per libretto forever.

**How it was found**, because the method is the transferable part: I asked
what re-attaches a libretto after a restart, found nothing did until someone
called `Libretto()`, and while checking who calls it noticed the sweep
calling `OpenLibretto` instead. The bug was two lines away from the question
that found it. **Ask what happens to the thing you built when the process
restarts, when the sweep runs, and when two callers arrive at once** — this
session found four bugs with those three questions.

## FINAL GATE (session 3), on `9d324c4f`, everything green

```
go build, go vet, go test ./... -count=1                        ok
-race -count=3 on store, figaro, angelus, actor, provider       ok
FIGARO_CRASH_TEST=1 acknowledged patches survive SIGKILL        ok
nix build .#default                                             ok
fleet: 12 arias, study, 300 patches      12/12
  history build 4.94 s   turn wall 5.28 s   control 0.16 s
  daemon PSS 49.1 M
live: scripts/live/studylive.sh, realstudy.sh                   ok
figwal: -race -count=3 (log/disk/segment/xwal), crashtest -long ok
```

**194 commits on `feat/incantations`.** figwal released four times, pinned at
`e44a843`, vendorHash reset and `nix build` proven each time.

### The ten faults of my own that measurement found, in one list

Because the count is the useful part, not any one of them:

1. a lost eviction race stranded bytes nothing could reclaim;
2. recency stamped a globally contended atomic on every read (reads got
   SLOWER with more readers);
3. the mirror copied its source's TOMBSTONE and sealed itself;
4. the idle sweep evicted a source and ORPHANED the fold, leaving a silently
   stale copy;
5. opening a libretto needed its source, so a study of a deleted form could
   not be dropped after a restart;
6. the migration guard skipped exactly the stores needing migration;
7. `doctor librettos` declined to repair the only case it existed for;
8. the libretto stump was drawn by `fig ls -g`;
9. `ForkWith` — the entry point the CLI uses — was not a refcount
   participant, while `Fork`, which only tests call, was;
10. the reconciliation sweep put a SECOND WRITER on a form.

**Four came from `scripts/live/`, one from a benchmark, one from a profile,
and four from asking three questions**: what happens when the process
restarts, when the sweep runs, and when two callers arrive at once.

### Optimism has to be sized for the contention it replaced (`609abccc`)

Taking the cast off the actor loop removed serialization between two castings
of one figaro and left optimistic retry in its place — sized, at five
attempts, for the world where that could not happen. Eight concurrent casts
exhausted it:

```
cast: study @c580c388: study: the board would not hold still
```

and each failure loses a study for a role **already pointed at the caster** —
a role pointing at a figaro that does not know it. 32 attempts with a small
jittered backoff now, so N writers of one board converge instead of colliding
in lockstep; each attempt costs one fsync and only under contention that used
to be impossible.

**The lesson, and it generalises past this change**: when you remove a
serialization point, the thing that replaces it inherits a load it was never
sized for, and the tests that passed did so because none of them exercised
the case the removed mechanism existed to handle. **Write the concurrent test
FOR THE PROPERTY THE OLD MECHANISM GUARANTEED**, not for the new code path.

### Retain once, not once per attempt (`dbf8704e`)

The retry loop took a reference and gave it back on every conflict, so eight
concurrent casts paid a retain and a release — two durable writes on the
libretto — per attempt each. **Contention should cost extra BOARD writes,
which are the thing being contended, not extra writes on a form nobody is
fighting over.**

The reference is taken once, before the first board write (§12.2.1's order
unchanged), held across retries, and handed back by a defer if the
declaration never lands. `TestAFailedStudyLeavesNoReference` holds that last
part: a study onto a SEALED board fails however often it retries, and must
leave the refcount exactly where it found it.

## HANDOFF GATE (session 4), all green

```
go build, go vet, go test ./... -count=1                       ok
-race -count=3 on store, figaro, angelus, provider             ok
FIGARO_CRASH_TEST=1 (acknowledged patches survive SIGKILL)     ok
nix build .#default                                            ok
fleet: 12 arias, study, 300 patches, vs a base run beside it   12/12
live: renderlive.sh (7 checks, on the wire)                    ok
```

The one that matters for this session is `renderlive.sh`, because it is the
only one that reads what the MODEL is sent. A green unit suite and a green
`doctor` said the switch worked while the studied block was frozen at its
first version on every real turn.
