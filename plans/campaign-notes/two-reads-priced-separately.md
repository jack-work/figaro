# The published view and the cold hop, priced separately for the first time

Aria 3a9225b1, 2026-08-18. Panel C at `5eb5fcb8`, `-cpu 1,4,16`, count=5,
machine quiet, under the bench lock. Reported **side by side, never as a
delta**: they answer different questions, and one number across them would
re-commit the error this branch just removed.

## The two benchmarks

    BenchmarkCachedLogReadFromParallel        reads INSIDE the window,
                                              tail-relative, re-read each
                                              iteration (the published view)
    BenchmarkCachedLogReadBelowWindowParallel reads at a fixed LT the writer
                                              trims past (the cold hop)

## Wall

                    cpu=1      cpu=4     cpu=16     shape
    published view   1847 ns   557.9    505.0 ns   FASTER with more readers
    cold hop         1603 ns   608.2    677.7 ns   turns over after 4

The published view keeps improving to 16 readers. The cold hop improves to 4
and then **degrades**. That divergence was invisible while both questions were
being asked by one benchmark.

## Contention -- and the two are not the same KIND

    published view   34.53 s total
                     19.92 s  57.70%  runtime._LostContendedRuntimeLock
                     14.61 s  42.30%  runtime.unlock
                     11.05 s  32.01%  (cum) cachedLog.ReadFrom
                     NOT ONE APPLICATION MUTEX IN THE PROFILE

    cold hop        156.89 s total
                    154.16 s  98.26%  MemLog.ReadFrom   <- a real sync.Mutex
                      1.82 s   1.16%  MemLog.Append

**The published view's 34.53 s is entirely RUNTIME lock contention.** Both flat
entries are Go's own allocator/scheduler locks. `cachedLog.ReadFrom` appears
only CUMULATIVELY, as the stack those runtime locks were sampled through --
not as a lock it waits on. That is consistent with the zero-lock probe, which
holds `writeMu` and watches the same call answer in 640 ns.

The cold hop's 156.89 s is the opposite: 98.26% in `MemLog.ReadFrom`, an
application mutex, exactly as its new name now says.

## What the published view's remaining cost actually is

Allocation pressure, not locking. The read copies its result --
`make([]Entry[T], end-start)` -- at **5,011 B/op**, from 16 goroutines at once.
That is enough to contend the allocator's internal locks, and the profile
shows precisely that and nothing else.

**Consequence for stage 10, and it is a limit on what the workstream can
claim:** this path has no application lock left to remove. Its cost is
allocation. A lock-removal campaign cannot improve it, and if a stage-10 commit
appears to improve it, the cause is somewhere else and must be found before
anything is claimed.

## What the fix bought, stated as two facts rather than one ratio

Under the OLD single benchmark, 97.9% of reads fell below the window and
`MemLog.ReadFrom` held 97.95% of a 134 s profile: the published view was never
being measured. Now:

  - the cold hop is measured honestly, and costs 156.89 s of real mutex delay;
  - the published view is measured for the first time, and shows **zero**
    application-mutex delay.

Both numbers are larger and smaller than the old one respectively, and neither
is comparable to it, which is why the commit that changed them declared every
straddling comparison void.

## One thing to watch

The fixed benchmark tolerates up to 1% of reads falling below the window (its
own guard fails above that). Those tolerated few are the only path by which an
application mutex could re-enter this profile. Today it shows none, so the
tolerance is not currently costing anything -- but a future change that widens
the fall-through would show up here as application-mutex delay appearing where
there is now only runtime noise. **That is the signal to watch, and it is
watchable only because the two benchmarks are separate.**
