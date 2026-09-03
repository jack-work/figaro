# The state-door measurement gate: proposal, not yet built

Aria 3a9225b1 (measurement arm, self-fork of 091d162e at turn 1),
2026-08-17. Branch under measure: `feat/layered-cache` @ 300bd36b,
then `feat/state-door` (9 stages) cut from it.

PREDICTIONS FIRST, CODE SECOND. Nothing in this file is built. It is
proposed for approval by 091d162e before a line of harness exists.

---

## 0. FIVE FACTS OBSERVED BEFORE PROPOSING ANYTHING

1. **The mutex census reproduces exactly.** My independent count over
   non-test Go: store 10 decls / 76 sites, provider 8/52, angelus 7/37,
   livelog/aria 3/46, figaro 4/31, sums to the briefed 32 decls / 242
   sites, and xwal_backend.go really does hold 31 of store's 76. The
   whole tree is larger: **62 decls / 386 sites** (cli 9, tool 8,
   outfit 3, otel 3, render 2, actor 2, wirelog/tape/logring 1 each).
   The briefed number is the five-package subset. Both numbers are
   right; they must never be quoted at each other. The census becomes a
   committed script so before/after are the same question.

2. **pprof is NOT armed.** `/run/user/1000/figaro/` holds angelus.pid,
   angelus.sock, angelus.startup, no `pprof.sock`. `StartPprof` only
   binds when `angelus.PprofEnv` is set. **The heap profile is blocked
   on a daemon restart with that env set.** That is a decision for the
   role bearer, not for me.

3. **The 929 MiB daemon is gone.** The current angelus (pid 3654247,
   started 11:25) is VmRSS 226 MiB / VmHWM 244 MiB, heap_alloc 140 MiB,
   heap_sys 232 MiB, 153 goroutines, 23 threads, 5 live arias.

4. **…but the gap is already present in miniature, and needs no
   waiting.** Metered right now: resident IR 11.7 MB + translations
   2.7 MB + segment cache 2.8 MB + UI window 2.1 MB = **19.4 MB metered
   against 146.6 MB heap_alloc**. The meters see 13% of the heap at
   5 arias, which is the same shape as 54 MiB against 830 MiB. The open
   item is therefore reproducible TODAY on a small daemon; it does not
   need a bloated one.

5. **Two hygiene items.** A `figaro-test` binary from `/tmp/fig-hushtest`
   is resident at 619 MB, and /tmp is tmpfs, i.e. RAM, i.e. the scratch
   rule broken by a previous session. Not mine, not killed, reported.
   And this worktree has uncommitted edits in `internal/figaro/agent.go`
   and `turn.go`: **no baseline will be taken from a dirty tree.**

---

## 1. WHAT THE GATE MEASURES

Four panels. Each stage of feat/state-door is measured before and after,
separately, on its own commit, so a revert gives back a known amount.

### Panel A: wall/alloc, the existing instruments, reused not reinvented
- `internal/figaro`: EmitFrame, EmitFrameStreamingPartial, ServerUpdateOnly,
  RegionMaterializationOnly, AgentRestoreHistory10000, InterruptRepair10000,
  AgentInfo10000, LiveFramePersistence
- `internal/compose`: OpenTurnFrame10/40/160, TurnAllFrames, StableBoundary64
- `internal/store`: CachedLogAppend, CachedLogReadFromParallel/Serial,
  FormReduceFold, FormOpenReplay, OpenLargeAria, Birth/Fork/Kill,
  Nodes, FormState10000, Vectors10000Branches
- `internal/form` (stages 8–9): Diff, Apply_SmallPatch, Tree*, Clone
Primary signal is **B/op and allocs/op** (deterministic); ns/op is
secondary and only believed through benchstat against a measured floor.

### Panel B: system resources
Per bench process: `VmHWM`/`VmRSS` from /proc/self/status at exit, peak
goroutines, peak fds, `runtime.MemStats` HeapSys/HeapInuse/Sys. Plus the
`doctor mem` counter set taken in-process on the same fixture, so the
metered/unmetered ratio is itself a tracked number per stage.

### Panel C: contention (gates the mutex workstream)
Parallel benches only, with `runtime.SetMutexProfileFraction(1)` and
`SetBlockProfileRate` set explicitly (an unset rate silently yields an
empty profile that reads as "no contention", the single easiest way to
fake this whole workstream). Report per-lock contention ns and delay
count, before and after, at -cpu=1,4,16.

### Panel D: correctness, not perf
`-race -count=3` on store/figaro/angelus/provider, plus identity oracles
(sameBytes/sameRaw, the countingLog decorator, TestHopGate_DoHopsReDecode).
Never mixed with Panel A numbers: a race build is slower by construction.

---

## 2. HOW EACH MEASUREMENT CAN FAIL

Stated before the harness so the harness can be built against them.

1. **The benchmark stops doing the work.** A stage that adds a cache or
   deletes a derivation can turn a measured op into a no-op, and a no-op
   is very fast. GUARD: every panel-A bench gets a companion
   "actually-does-the-work" assertion in the shape of
   TestEmitFrameBenchmarkActuallyComposes, bytes composed, rows decoded,
   records appended, counted by the countingLog decorator. Four of that
   test's assertions were wrong before they were right; the new ones get
   the same suspicion.
2. **A deleted benchmark reads as an improvement.** Stage 4 deletes the
   derivation and with it, plausibly, the benchmarks that measured it.
   GUARD: the panel is a FIXED NAMED LIST. A missing benchmark is RED,
   printed as `ABSENT`, and requires a written reason, never a blank
   cell.
3. **The fixture changes under the format-breaking stages (3, 6, 8, 9).**
   Before and after then measure different work and the comparison is
   void. GUARD: a fixture-parity assertion per stage (same turn count,
   node count, composed byte total at the seam), and where the record
   shape genuinely changes, the record byte delta is reported BESIDE the
   time, so "faster" cannot hide "doing less".
4. **Machine noise.** 16 cores shared with a browser, k3s and gopls.
   GUARD: an A/A run (the same commit twice) establishes the floor
   before any A/B is believed; benchstat with -count>=10; any delta
   inside the floor is reported as "no change", not as a small win.
5. **-race numbers leaking into perf claims.** GUARD: separate binaries,
   separate report sections, never in the same table.
6. **A lock removed with no contention behind it.** The profile only
   shows contention the benchmark creates; a single-threaded bench turns
   every lock into 0 ns. GUARD, and this is the workstream's hard rule:
   **no before-contention number, no perf claim.** A removal whose
   before-contention is 0 is a LEGIBILITY change and must be labelled
   one. It may still be right, it is just not mine to call fast.
7. **RSS lags the heap.** Freed heap is not returned promptly; a real
   improvement can show 0 in VmRSS for minutes. GUARD: report
   heap_alloc / heap_sys / Sys / VmRSS / VmHWM together, and call
   `debug.FreeOSMemory()` before the RSS reading, stating that we did.
8. **Stale source in the nix build.** The flake archives only tracked
   files. GUARD: `git add` + `git status --porcelain` asserted empty in
   the harness itself, refusing to run against a dirty tree.

---

## 3. HOW EACH PANEL IS CANARIED

A canary is a mutation that MUST move the number. It is applied, built,
run, and reverted, and **the build success is recorded**, a canary that
does not compile proves nothing, which cost this campaign a session once.

- Panel A time: insert one extra full recompose into the measured path →
  the bench must regress >20%. If it does not, the bench is not on that path.
- Panel A allocs: `make([]byte, 1<<20)` in the path → B/op must move ~1 MiB.
- Panel A work: disable a fork seed → the identity oracle must go
  SHARED→MINTED. (Proven instrument, reused.)
- Panel B: allocate and retain 64 MiB in the fixture → VmHWM must move ~64 MiB.
- Panel C: wrap the measured op in a global mutex → that mutex must appear
  as the top contender. An empty profile here means the profile RATE is
  misconfigured, not that the code is contention-free.
- Panel D: the census script, add one `sync.Mutex` to a file → count +1.

---

## 4. PRE-REGISTERED PREDICTIONS (score these publicly)

Noise floor, first: A/A wall within **±3%**, and B/op + allocs/op
**exactly equal** on the deterministic benches. If A/A shows drift in
allocs, the harness is wrong and nothing else in this list is readable.

| stage | prediction |
|---|---|
| 1 one fold | flat within noise everywhere; FormReduceFold 0 to −8%; allocs delta 0 |
| 2 one door per append | a small **REGRESSION**: +0–5% on CachedLogAppend, allocs +0 or +1/op |
| 3 door stamps TurnID / closes the call | InterruptRepair10000 −5–15%; record +8–16 B → resident_ir_bytes +2–4% |
| 4 delete the derivation (5 full-log reads) | **the big one**: AgentRestoreHistory10000 ≤−30%, InterruptRepair10000 ≤−50%, OpenLargeAria −10–25%; first net-negative Go LOC of the campaign. **If it is under −10%, those 5 reads were already served warm, and that is a finding, not a failure.** |
| 5 one in-flight turn | EmitFrameStreamingPartial 22 allocs → 18–22, **not below 18**; wall flat |
| 6 one record shape | resident_ir_bytes −5–15%, decode −5–10%. FALSIFIER: a uniform record could be BIGGER; I predict it is not |
| 7 coordinates → _meta index | FormState10000 / Vectors10000Branches improve; the LT lookup stops scanning |
| 8–9 one value model, one patch vocabulary | form Diff −10–30%; resident_form_patches down; Apply_SmallPatch flat-to-better |
| mutex workstream | **≤40% of the 242 sites show any contention at all**, contention is Pareto (1–2 locks own most of it, likely in xwal_backend.go and livelog/aria), and the whole workstream moves no existing benchmark by more than **5%**. Deliberately deflationary: I expect this to buy legibility, not speed, and I want to be on record before the numbers. |
| RSS gap | the missing bytes are NOT in the four metered caches and NOT one leak; the heap profile names **≤5 call sites for ≥60% of inuse_space**, and the residue is GC lag against the 512 MiB soft limit plus unmetered per-aria retention. Testable today at 127 MiB unmetered on a 5-aria daemon. |

---

## 5. WHAT I NEED BEFORE BUILDING

1. Approval of the panels, the failure list, and the canaries.
2. A ruling on the pprof restart (item 0.2), I will not restart the
   live daemon on my own authority.
3. A clean commit to baseline from: the worktree is dirty with someone
   else's edits, so I will cut my own worktree on /var/tmp at 300bd36b
   and never touch the product code.
