# The form patch view: BEFORE and AFTER

Measured 2026-08-11 on this box (AMD Ryzen 7 5800X, 16 threads, go1.26.5,
54 GB RAM). Everything below is measured output. Nothing is estimated.

Base revision: `8b12f128`. The BEFORE tree is a worktree at that revision
(`/home/gluck/dev/figaro-qua/perfbase`) carrying the same benchmark and probe
files, implemented against the old API through one seam function.

## The change

`store.Form.Patches()` returned a **copy of every patch a form had ever
taken**. `Agent.formAccessor` / `Agent.studyAccessors` called it through
`Backend.FormPatches` **once per form per provider Send** (the board plus
every studied form), and then walked a fresh forward cursor to the range the
projection actually asked for, which is one patch or none on a warm turn.

It is replaced by `Form.PatchesBetween(after, upTo]`: a binary search into the
published, immutable patch array returning a **capped sub-slice**. No copy, no
retained cursor. The safety argument is the one already written at
`Form.commit`: a published `formState` is immutable, its slice header carries
its own length, and the single writer only appends past that length or
reallocates. The three-index slice (`ps[lo:hi:hi]`) stops a caller's `append`
from landing in the writer's array.

## What was measured

| deliverable | file | what |
|---|---|---|
| the seam | `internal/store/formview_bench_test.go` (`accessorRange`) | the ONE place a bench or probe asks "what changed between these stamps" |
| microbench | same file | delta and whole reads over 100/1000/10000 patch histories; 8 and 50 studied forms |
| identity | `internal/store/formview_test.go` | differential against the verbatim old copy-then-walk, over every range |
| real data | `internal/store/realform_probe_test.go` | a copy of the author's live store: does it open, and what does one read cost on real boards |
| live load | `scripts/ariastress.sh` | 12 arias, one daemon, one studied form, real provider; PSS/Swap and daemon heap |
| telemetry | `figaro.form.patches.{returned,history}` | the pair that makes the claim a number in production |

## 1. Microbenchmarks (`benchstat`, `-count=6`)

```
                           │  before  │           after            │
                           │  sec/op  │   sec/op     vs base       │
FormDeltaPerSend100          667.70n     64.47n   -90.35%  (p=0.002)
FormDeltaPerSend1000        7716.50n     67.79n   -99.12%  (p=0.002)
FormDeltaPerSend10000      64025.00n     72.02n   -99.89%  (p=0.002)
FormWholePerSend100         3061.00n     63.46n   -97.93%  (p=0.002)
FormWholePerSend1000       35890.50n     66.81n   -99.81%  (p=0.002)
FormWholePerSend10000     454643.00n     71.14n   -99.98%  (p=0.002)
StudiedSetPerSend8x500      30709.5n     582.6n   -98.10%  (p=0.002)
StudiedSetPerSend50x500     146.379µ     4.301µ   -97.06%  (p=0.002)
FormState10000                57.35n     57.08n        ~   (p=0.394)   <- control
geomean                       11.74µ     133.6n   -98.86%

                           │   B/op    │        B/op   vs base      │
FormDeltaPerSend100          4.047Ki      0.000Ki  -100.00%
FormDeltaPerSend1000         40.07Ki      0.00Ki   -100.00%
FormDeltaPerSend10000       403463.5        2.000  -100.00%
FormWholePerSend10000      2258754.5        2.000  -100.00%
StudiedSetPerSend50x500    1026630.5        7.000  -100.00%
```

`FormState10000` is the **do-nothing control**: it does not touch the changed
path and does not move. Without a control in the table, an optimization gets
credit for whatever the machine was doing that afternoon.

The old cost is linear in history and linear in observed forms, which is the
slope that matters: 50 studied forms at 500 patches cost **1.0 MB and 146 µs
per Send**, and a tool loop is many Sends per turn.

## 2. On real boards (a copy of the live store, 273 MB, 629 nodes)

```
store opened in 16.3 ms; 623 boards folded (23,365 keys); full replay 2.1 s
total patches across all boards: 5,606   mean 9.0   max 99

FORM         KIND          HISTORY   DELTA ns/op   DELTA B/op   RETURNED
                                     before after  before after
84de420c     conversation       99     698    73    4144     0        1
ed3915b1     conversation       99     724    74    4144     0        1
@4947b6b1    form               92     715    76    4144     0        1
```

**Real boards are short.** The mean is nine patches and the longest in 629
nodes is 99. So on today's data the per-read saving is ~620 ns and 4 KB, not
the 99.98% the 10,000-patch bench shows. Say this out loud: the resident-bytes
table from the memory probe puts the form at ~0.2% of a real aria's footprint,
and a reviewer who reads only the microbenchmark will over-credit this change.

The win is real but it is **CPU and allocation churn per Send**, and it scales
with two things that are both about to grow: how long a board lives, and how
many forms one figaro observes. It is a precondition for the libretto, not a
memory fix.

### A finding, unrelated to this change

Five conversations answer `index not found` for their form channel, with empty
`_meta` beside them: `2ee6db0b`, `4a2aa05d`, `e1560e1b`, `e6e3a96c`,
`f443d3fb`. They read identically before and after, and identically in the
**live** store (`figaro state 2ee6db0b` → `index not found`), so this is not a
copy artifact. Stillborn arias whose board was never written. The probe
tolerates a stated count via `FIGARO_PROBE_ALLOW_UNREADABLE`.

## 3. Live load: 12 arias, one daemon, one studied form (300 patches)

Real provider (copilot / claude-sonnet-5), isolated `FIGARO_STATE_DIR`,
`FIGARO_RUNTIME_DIR`, `FIGARO_CONFIG_DIR`.

|                         | before        | after         |
|-------------------------|---------------|---------------|
| turns answered          | 12/12         | 12/12         |
| turn wall               | 4.41 s        | 4.53 s        |
| control (12 × `ls -j`)  | 0.15 s        | 0.17 s        |
| daemon PSS idle         | 37.9 M        | 37.8 M        |
| daemon PSS loaded       | 58.3 M        | 56.8 M        |
| daemon PSS_anon loaded  | 32.3 M        | 30.6 M        |
| daemon Swap             | 0             | 0             |
| heap_alloc              | 13.3 M        | 14.9 M        |
| goroutines              | 93            | 93            |

**Within noise, and that is the honest result at this scale.** Twelve arias
each taking one turn against a 300-patch form is roughly 3.6 MB of copying
spread over ten seconds of mostly-network time. The process-level signal
appears when Sends-per-turn and forms-per-figaro multiply, which is exactly
what this path is being cleared for.

PSS rather than RSS throughout: RSS counts every page of shared binary text
once per process, and a previous fleet measurement on this box saw 26.57 GB
RSS against 15.78 GB PSS. Swap is reported beside it because an idle daemon
once showed 300 MB resident with 800 MB paged out.

## 4. Telemetry

Two new instruments, wired at the one place that knows both numbers:

- `figaro.form.patches.returned`: what a range read answered with.
- `figaro.form.patches.history`: how long the history behind it was.

The **pair** is the point: before the view, a read returned one patch and
copied the whole history, and no instrument in the binary could say so: the
only metric figaro had was `figaro.request.duration`, and a copy hides
comfortably inside a network round trip.

From the live 12-aria run (`state/metrics.jsonl`, 72 reads):

```
returned  Count=72  Min=0  Max=301  Sum=3672   buckets: 12 at 0, 48 at 1-5, 12 at 251-500
history   Count=72  Min=5  Max=301  Sum=7464   buckets: 48 at 1-5,   24 at 251-500
```

Read it as designed: twelve first-sightings legitimately fold the whole
300-patch form (that IS the baseline), and every subsequent read returns one
patch out of a 301-patch history. A `returned` distribution that starts
tracking `history` is the regression alarm for anything built on this path
later.

## 5. Reproducing

```sh
# micro, both trees, identical flags
go test ./internal/store -run XXX -bench 'FormDeltaPerSend|FormWholePerSend|StudiedSetPerSend|FormState10000' \
  -benchmem -count=6 > after.txt
benchstat before.txt after.txt

# real data: a COPY, never the live store
box=$(mktemp -d); chmod 700 "$box"
cp -a --reflink=auto ~/.local/state/figaro/arias "$box/arias"; chmod -R go-rwx "$box"
FIGARO_PROBE_ROOT=$box/arias FIGARO_PROBE_ALLOW_UNREADABLE=5 \
  go test ./internal/store -run 'RealStoreOpens|RealFormPatchCost' -v
rm -rf "$box"          # it holds conversation history

# live load
./scripts/ariastress.sh --label after --arias 12 --study --study-patches 300 --keep
```

## Traps this run walked into, recorded so the next one does not

1. **The benchmark that measured the wrong question.** `BenchmarkFormPatches10000`
   timed "copy the whole history" faithfully for months. Nothing timed "what a
   Send actually asks", which is where the cost was. A benchmark named after
   the API rather than the question is how an O(n) path stays invisible.
2. **The fake accessor in `observation_bench_test.go` returns a sub-slice** , 
   the shape we wanted, not the shape production had. The observation suite
   therefore reported the cost of the fix while the bug was live.
3. **`bc` is not on this box.** The first stress run printed blank timings and
   exited 0. Arithmetic in a measurement script needs the same suspicion as
   the measurement.
4. **`pgrep -f` matches the script's own command line**, which is how a
   teardown loop reports processes remaining forever.
5. **Ephemeral arias (`send -er`) touch no board**, so the first load harness
   exercised everything except the path under test. `live_arias: 0` in the
   output was the tell.

## 6. After the incantations (2026-08-11, same branch)

The incantation work touches the study renderer and the CLI, not the store's
read path, and the numbers say so. Same toolchain, same machine:

```
FormDeltaPerSend100      64.47n -> 68.06n   +5.58%
FormWholePerSend10000    71.14n -> 70.94n        ~
StudiedSetPerSend50x500  4.301u -> 4.258u        ~
FormState10000 (control) 57.08n -> 57.21n        ~
```

A few nanoseconds on the delta reads, nothing anywhere else, control flat.

The renderer's own numbers, which are where the feature lives:

```
StudyReminderEmptyBoard     2.001u   1.707Ki   (what it cost before the feature)
StudyReminderNoIncantation  2.035u   1.707Ki   (+34ns, +0 allocs: every aria today)
StudyReminderWithIncantation 3.505u  2.835Ki   (only arias that set the key)
StudyReminderNoStudyEvent    4.44n   0 B       (a message with no study event)
ForkReminderOrdinaryMessage 12.17n   0 B
```

Three things were deliberate:

- **The board is not consulted unless the message carries a study event.** A
  non-studying aria pays 4.4ns per message, which is the type switch.
- **A well-formed incantation decodes straight into its struct**, no
  intermediate map, no per-field unmarshal. The first version built a map per
  read and cost 2.3x the render; only a value that FAILS the strict decode now
  pays for the diagnosis of why. A strict decoder is also what routes a typo
  to the diagnostic path instead of silently dropping it.
- **The 1.5us for arias that set the key is paid once per message**, not once
  per turn: the encoded block lands in the per-LT translation cache.

### A benchmark trap, for the record

`nix develop` ships its own Go. The first after-run inside the devshell showed
a uniform +8 to +10% across every benchmark, INCLUDING `FormState10000`, which
this branch does not touch. The control is what said "toolchain, not code".
Compare like with like or do not compare.
