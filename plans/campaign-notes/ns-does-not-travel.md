# Bytes and allocations travel between machines. Nanoseconds do not.

Aria 3a9225b1, 2026-08-17, while re-measuring the memory campaign's fixtures
on a second machine to establish a baseline for the state-door stages.

## The observation

Same source (`feat/layered-cache` @ 300bd36b), same fixtures, two hosts:

    fixture                        campaign recorded      re-measured here
    streaming frame                ~12,000 ns             10,400 ns
                                    9,543 B                9,545 B
                                       22 allocs              22 allocs

    still frame (rounds=64)        ~6,000 ns              5,389 ns
                                    1,352 B                1,353 B
                                       21 allocs              21 allocs

    stableBoundary64               1,231 ns               1,188 ns
                                        0 B                    0 B
                                        0 allocs               0 allocs

Wall time moved by 10-15%. **Allocation counts are identical. Byte counts
differ by one and two bytes** -- fixture-dependent map iteration, not noise in
the instrument.

## Why this matters for how the campaign's numbers are read

The campaign's headline is "416,537 ns and 788 allocations became ~12,000 ns
and 22". On another machine the same code is ~10,400 ns, and on a slower or
busier one it will be 15,000. **The ratio survives; the absolute does not.**
Anyone re-running these fixtures and getting a different nanosecond count has
not found a regression, and anyone quoting the nanosecond count as a property
of the code is overstating what was measured.

The allocation numbers are the ones that carry, and they are the ones the
campaign's real claims rest on anyway: 788 -> 22 allocations is a statement
about what the code DOES. 416,537 -> 12,000 ns is a statement about what one
machine did on one afternoon.

## What follows, practically

1. **B/op and allocs/op are the primary signal** in every panel of the
   state-door gate. They are deterministic: on identical source, across runs
   and across machines, they do not drift. Any difference is real.
2. **ns/op is secondary** and is only read against an A/A floor measured on
   the same host, in the same session, with the machine otherwise quiet.
3. A ns/op comparison across machines, across sessions, or across a change of
   TMPDIR filesystem is not a comparison. (See tmpfs-benchmarks.md: store
   benchmarks were timing a RAM disk.)
4. When quoting a wall-time improvement, quote the host and the floor with it,
   or quote the allocation number instead.

## The uncomfortable corollary

Two of the campaign's most-quoted numbers -- the 26x on the frame and the
"still frame" 6,000 ns -- are wall times. The 26x is a ratio measured on one
host in one session and is as robust as ratios usually are. The 6,000 ns is
an absolute, and it was already flagged in the campaign's own summary as true
but misleading for a different reason (it is the frame in which nothing
changed). It is misleading in a second, independent way as well: it is one
machine's afternoon.

Neither correction diminishes the work. Both are the difference between a
number that survives being re-read and one that quietly stops being true.
