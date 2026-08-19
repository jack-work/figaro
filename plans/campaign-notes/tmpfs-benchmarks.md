# Every store benchmark in this repo has been measured against a RAM disk

Aria 3a9225b1, 2026-08-17. Found while building the state-door measurement
gate; reported separately because IT IS NOT ABOUT THE GATE and it affects
numbers already in the record.

## The finding

`b.TempDir()` returns a directory under `$TMPDIR`, and `$TMPDIR` is unset in
every shell this project's benchmarks have been run from, so it defaults to
**/tmp**. On this machine /tmp is **tmpfs**:

    $ df -hT /tmp /var/tmp
    tmpfs          tmpfs   28G  685M   27G   3% /tmp
    /dev/nvme1n1p2 btrfs  932G  269G  660G  29% /var/tmp

685 MB of this machine's RAM is currently held as files under /tmp.

Two consequences, and the second is worse than the first.

1. **Every store benchmark that opens a store on disk has been timing a RAM
   disk.** `BenchmarkOpenLargeAria`, `BenchmarkBirth`, `BenchmarkFork`,
   `BenchmarkKill`, `BenchmarkCachedLog*`, `BenchmarkNodes`,
   `BenchmarkFormState10000`, and everything else that calls `b.TempDir()` and
   then writes a WAL. No fsync in those numbers ever touched a real device.
   The numbers are not wrong -- they are answers to a different question than
   the one they appear to answer, and the difference is invisible in the
   output.

2. **The fixture lives in the memory being measured.** A benchmark that writes
   a large store into tmpfs grows the machine's page cache accounting as RAM,
   in the same process's cgroup, while Panel B is asking what that process
   costs in RAM. Resource measurement and fixture storage were the same pool.

## How it surfaced, and a correction to my first account of it

Not by inspection. `BenchmarkOpenLargeAria` failed outright in the first panel
run:

    --- FAIL: BenchmarkOpenLargeAria
        openperf_test.go:60: xwal: store /tmp/BenchmarkOpenLargeAria2878332039/001
        already has a writer (flock: resource temporarily unavailable)

**I first attributed that flock to stale leftovers in /tmp from an earlier
session. THAT WAS WRONG**, and I only learned it because the benchmark failed
again, identically, after the move to `/var/tmp`:

    openperf_test.go:60: xwal: store /var/tmp/BenchmarkOpenLargeAria3117003772/001
    already has a writer

A fresh `b.TempDir()` on a real device, and the same error. So the cause was
never the filesystem. It is in the benchmark:

    func seedLargeAria(tb, ...) (string, string) {
        root := tb.TempDir()
        be, err := NewXwalBackend(root, 0)   // takes the writer flock
        ...                                   // and is NEVER closed
        return root, conv
    }

    func BenchmarkOpenLargeAria(b *testing.B) {
        root, conv := seedLargeAria(b, 600, 2048)
        s, err := OpenXwalStore(root, 0)     // second writer on the same dir
        if err != nil { b.Fatal(err) }       // <- always
    }

The seeding backend holds the lock for the life of the test binary. There is
no `Close` and no `b.Cleanup` anywhere in the file -- the only `Close` in it
is on the node inside the loop. **This benchmark has never produced a number**,
on either filesystem, and it is a separate defect from the tmpfs one.

Two things follow, and the second matters more than the first.

- The finding above about tmpfs stands entirely on its own evidence (`/tmp` is
  tmpfs; `b.TempDir()` defaults to it; store benchmarks therefore time a RAM
  disk). It never depended on this failure, which is lucky, because this
  failure was not what I said it was.
- **My first explanation was the convenient one.** "Stale leftovers" required
  nothing to be wrong with the code and fit the story I was already telling
  about /tmp. The correct explanation required reading `seedLargeAria`. I
  reached for the first and was saved by the panel's NO RESULT rule, which
  refused to let a benchmark disappear quietly and put the same failure in
  front of me a second time.

Related, and already flagged: a 619 MB `figaro-test` binary from
`/tmp/fig-hushtest` was resident on this machine, which is 619 MB of RAM held
by a test artefact nobody owns.

## The fix, and what it costs

All measurement panels now run with `TMPDIR=/var/tmp`, which is on
`/dev/nvme1n1p2` -- a real device with 660 GB free.

**Expect store benchmarks to get SLOWER, and expect that to be correct.** Any
before/after comparison that crosses this change is void: /tmp numbers and
/var/tmp numbers are not comparable, and a stage measured on one against the
other would show a "regression" that is nothing but a filesystem.

Two things follow for anyone re-reading old numbers:

- **Store benchmark numbers recorded before 2026-08-17 were taken on tmpfs**
  unless the runner set TMPDIR explicitly. They remain valid relative to each
  other and invalid as absolute costs.
- The repo's own benchmarks would benefit from setting `TMPDIR` themselves, or
  from a helper that puts `b.TempDir()` on a real device, so the answer does
  not depend on the shell that happened to launch the run. That is a product
  change and is NOT mine to make; it is filed here for whoever owns it.

## The general form of the lesson

The measurement asked "how long does opening a large aria take". The apparatus
answered "how long does opening a large aria take, when the disk is not a
disk". Nothing in the output said so. An instrument that silently substitutes
one question for another is the same failure mode as a benchmark that stops
doing the work -- which is why the gate now asserts its environment rather
than inheriting it.
