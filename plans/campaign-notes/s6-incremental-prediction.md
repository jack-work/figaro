# S6, fix 2: what incremental composition is predicted to buy

Pre-registered by aria 53289ae2 on 2026-08-15, BEFORE the composer was
written and BEFORE its benchmark was run, at the role bearer's order: a
pre-registered expectation turns a number into a test with a verdict,
instead of a number that can be rationalized either way.

## The baseline it is predicted against

composeTurn, one frame, 64 rounds, 200-line tool outputs, after the
tailBound fix (636ad732) and therefore NOT containing it:

    359,661 ns/op    175,865 B/op    663 allocs/op

128 nodes (one prose, one tool per round). sizeof(livedoc.Node) = 280 B.

## The prediction, in two parts, because the floors are different

PART A -- the composition itself (internal/compose). Every frame
recomposes 128 nodes of which at most 2 can have changed. Incremental
composition should therefore cut the per-frame COMPOSITION work by
roughly (unchanged nodes / total nodes) = 126/128, leaving one round's
composition plus a fixed floor.

The floor is the returned slice: the caller is handed one []livedoc.Node
of the whole region, so 128 x 280 B = 35,840 B is paid every frame no
matter how little changed. PREDICTION: compose-level allocation falls to
approximately 36-38 KB per frame, i.e. about one node-slice copy, and
does not fall below it.

PART B -- composeTurn (internal/figaro), which is what the baseline above
measures. It also materializes the region every frame: ReadFrom copies
128 store.Entry values, and the loop copies them again into a
[]message.Message. That plumbing is untouched by a composer.

PREDICTION: composeTurn per-frame allocation falls from 175,865 B to
roughly 90-110 KB -- a 1.6x to 2.0x reduction, NOT the 60x the node
arithmetic alone would suggest -- and the residue is region
materialization, not composition.

I am predicting a partly disappointing number on purpose. If composeTurn
comes in far BELOW ~90 KB, something other than the composer changed and
I must find out what before claiming it. If it comes in far above, the
composer is not skipping what it believes it is skipping -- the likeliest
cause being something downstream that still touches every node.

## What that implies for the layered cache, if the prediction holds

Then "derive a range" is only half paid by the composer: the other half
is that the agent reads the WHOLE open region out of the log on every
frame to hand it to the projector. Cutting that is a read-side change
(hand the projector the suffix and let it own the prefix), and it is the
same primitive again. Do not fold it into fix 2; measure it separately,
or the composer's number silently contains it -- the same rule that put
the tailBound fix in its own commit.

## Verdict

Recorded after the fact, below this line, whatever it says.

## Verdict, recorded 2026-08-15 after the fact

PART A was not measured directly at the compose level; part B was, and
it is the one the prediction was stated against.

composeTurn, one frame, 64 rounds, 200-line outputs:

    predicted   90-110 KB   (1.6-2.0x reduction)
    actual      113,224 B   (1.55x reduction)

Marginally OUTSIDE the band, on the disappointing side. The prediction
was right about the kind of residue and slightly optimistic about its
size: the floor is real and it is the one that was named -- one []Node
of the whole region handed to the caller every frame, plus the region
materialized twice by composeTurn before the composer is called.

TIME WAS NOT PRE-REGISTERED, and it moved far more than the bytes:
359,661 -> 24,017 ns, 15.0x. That asymmetry is explained rather than
claimed: what was eliminated is per-node string building, map building
and Sprintf; what remains is memmove. Bytes moved by the ratio of what
is still copied; time moved by the ratio of what is no longer computed.
An unpredicted 15x deserves the same suspicion as an unpredicted miss,
so it is stated as an explanation that could be wrong, not as a win.

## The attribution the two commits were split to make possible

One whole 64-round turn at 11 frames per round, measured at each commit:

    stage                     ns/op         B/op       allocs/op
    before (1245d741)   154,077,255  570,487,016         334,761
    tailBound (636ad732)148,911,634   88,766,833         285,229
    composer (1f0c0df6)  18,104,624   56,422,662          36,954

THE TWO FIXES WON DIFFERENT THINGS, and measured together they would
have been indistinguishable:

  - tailBound took the BYTES: 570.5 MB -> 88.8 MB, 6.4x, while moving
    time only 3%. It was allocation, not computation.
  - the composer took the TIME: 148.9 ms -> 18.1 ms, 8.2x, for a
    further 1.57x on bytes.

Total 8.5x time, 10.1x allocation, 9.1x allocation count. If either has
to be reverted, its own contribution is known. That is the whole
argument for separate commits, and it is the first time in this campaign
the argument has been paid off with numbers.

## What this campaign learned about its own discipline

Measured together, the two fixes are one number reading "8.5x" and nobody
could say which half a revert gives back. Measured apart: the clamp took
the BYTES and 3% of the time; the composer took the TIME and 1.57x more
bytes. That is the first time this campaign priced its own discipline
instead of arguing for it.

## Correction: the residue is GC, not memmove

The 15x time win was explained as "what remains is memmove". A CPU
profile of the after-state (150,000 iterations, 7.57 s of samples;
a first run at 150 ms was under-sampled and discarded) says otherwise.

    runtime.memmove                9.38%
    runtime.scanObject            16.51%
    runtime.tryDeferToSpanScan     9.38%
    runtime.typePointers.next      8.45%
    runtime.scanblock              7.40%
    runtime.bulkBarrierPreWrite    6.87%
    runtime.scanObjectsSmall       5.15%
    runtime.memclrNoHeapPointers   4.76%
    compose.(*Incremental).valid   5.94% (cum, mine)
    compose.stableBoundary         1.32% (mine)

CONFIRMED: the string-building, map-building and Sprintf paths are gone;
not one appears. WRONG: memmove is 9.4%, not the residue. Roughly half
the profile is garbage collection -- marking, write barriers, zeroing --
because a pointer-dense []livedoc.Node of the WHOLE region (128 nodes x
280 B, each full of string headers and slices) is still allocated, zeroed
and traced every frame.

The memo's validity guard costs 6-7%. That is the price of being able to
drop rather than lie, and at 15x it is affordable.

WHAT IT MEANS FOR THE READ-SIDE CHANGE: the floor is pointer DENSITY per
frame, not bytes copied. The target is to stop minting a whole-region
[]Node per frame at all. And there is a second one downstream that the
byte number hid: aria.Server.Update does
`s.open.nodes = append([]livedoc.Node(nil), nodes...)`, so the same
pointer-dense region is allocated and traced a SECOND time every frame,
inside the server. Two whole-region allocations per frame, both traced.

## (a) pre-registered, in OBJECTS, because that is what the machine charges

Written before the read-side change, after measuring the WHOLE frame for
the first time. Baseline, one frame, 64 rounds, 200-line outputs:

    composeTurn      24,017 ns   113,224 B    23 allocs
    Server.Update    56,876 ns    65,536 B   769 allocs
    whole frame      78,644 ns   177,933 B   788 allocs

Three targets, in measured order of cost:

  1. delta() builds two maps and a slice for EVERY node before asking
     whether that node changed: 6 allocations x 128 nodes = 769/frame.
  2. composeTurn mints a pointer-dense whole-region []Node per frame:
     128 x 280 B, allocated, zeroed and GC-traced.
  3. Update retains it AGAIN: append([]livedoc.Node(nil), nodes...),
     a second whole-region allocation, also traced.

The composer already knows which nodes changed -- it decided. Handing
that range over turns Update's diff from cheaper into UNNECESSARY, and
the retained copy from a copy into a reference. The server's copy exists
to make the previous frame immutable under a reader, so the replacement
must be a slice the composer promises never to mutate, published as a
successor. That law is already pinned twice in internal/store.

PREDICTION, per frame at 64 rounds:

    allocations   788      -> 20-40      (the dominant win; ~2 changed
                                          nodes' deltas plus fixed cost)
    bytes         177,933  -> 10-25 KB
    time          78,644   -> 8-15 us

Objects retained and traced per frame: 2 whole-region []Node (256
pointer-dense elements) -> 0 new whole-region allocations in the steady
state, since both sides hold the same immutable slice.

If allocations do not fall below ~100, the changed-range report is not
reaching delta() and something still walks every node. If bytes fall
much below 10 KB, a copy that some reader depends on has been removed
and the held-view law is the thing to check first, not the number.

## For (c), before it is designed

A byte-budgeted cache of POINTER-DENSE values is GC pressure the budget
cannot see. forest.Cache[U] holds runs of U; with U = livedoc.Node at
280 B carrying string headers and slices, two caches holding equal BYTES
can cost wildly different collector time, and "resident 9.8 MiB of 16.0"
says nothing about it. The layered cache needs a SECOND number in
doctor mem -- objects or pointers retained -- or it will bound the cheap
axis and ignore the one that hurts.

## (b) memoKey's blindness: verdict, and the design that stays unbuilt

Recorded by aria 091d162e, 2026-08-15, at the role bearer's order.

THE QUESTION. memoKey covers message content only. A tool node renders
its Output from `partials`, its Input from `argPartials`, and its three
timestamps from `timings` -- three maps the key never reads. Arguments
are keyed by COUNT, not value. So the memo cannot see any of the things
that move a running tool's node.

THE CONTRADICTION THAT RESOLVED IT. TestIncrementalEqualsWholesale-
AtEveryFrame drives "output streaming under a running tool" and passes.
Instrumented: turnScript(4) is 48 frames with ZERO frames in which the
memoized prefix contains a message that depends on a live map. The
fixture never presents the hazard. The pass was never evidence about
blindness -- the same shape as the pty case's surviving mutant, one
level down.

VERDICT: LATENT, NOT LIVE. Two constructed shapes broke the split rule
(a duplicate result id; a timing stamped after its result went
durable). Neither is reachable today, and not because of anything in
internal/compose: assembleToolResults writes one result per call in one
tic, repairInterruptedTail refuses to touch a partially answered call,
and finishToolTiming is stamped on toolEnd before the tic exists.
Guarded from a distance, by code that had never heard of the memo.

FIXED (62505d20): the boundary matches results to invokes by id instead
of counting them. 0 B, 0 allocs; 934 ns -> 1231 ns per frame at 128
messages, ~1.3% of a 24 us frame. A map per call was rejected on
instruction: delta() minting maps per node was 769 of 788 allocations
one layer down, and this campaign spent four days deleting it.

PINNED (61927498): the two distant guards are asserted in
internal/figaro, with failure messages that name the guard rather than
the symptom.

NOT BUILT, AND THIS IS THE DESIGN IF IT EVER IS. Keying the memo on
what it RENDERS -- partials, argPartials and timings for every
unresolved invoke in the prefix -- is the robust answer: it removes the
action at a distance entirely, and the boundary rule could then be
relaxed rather than tightened. It is unjustified without a number,
because it moves cost onto EVERY frame (hashing or comparing the live
tail of each running tool) to buy safety against shapes production does
not currently produce. Build it when a measurement says partial-driven
invalidation is the bottleneck, or when internal/figaro is about to
gain a path that appends a call's result in more than one message --
e.g. streaming tool results, or per-call tics. Either of those makes
this the cheap answer and the id-matching boundary the expensive one.

## (d) pre-registered: the read-side change, before the code

By aria 091d162e, 2026-08-16. Baseline, one frame, 64 rounds, 200-line
outputs, AFTER the composer and the changed-range report:

    whole frame                16,211-20,577 ns   71,734 B   30 allocs
    region materialization       4,779-7,746 ns   45,696 B    2 allocs
    share                              ~30% time      64% bytes

The region is re-read and re-copied every frame: ReadFrom copies the
entries, the loop copies them again into []message.Message. In the
steady state at most two of 128 messages are new.

PREDICTION, holding the region across frames and extending it by the
entries appended since the last one:

    bytes    71,734  -> 26-32 KB   (the 45.7 KB copy, minus a per-frame
                                    residue for the in-flight message)
    time     16,211  -> 11-14 us
    allocs   30      -> 28-33      (two big copies leave; one small
                                    copy-on-append arrives)

If bytes do not fall by roughly 40 KB the memo is not being reused and
something invalidates it every frame. If allocs FALL far below 28, the
in-flight message is being appended into the memo's backing array
rather than a copy of it -- which would be a mutation of a slice a
reader may hold, the exact law pinned twice in internal/store, and a
correctness bug wearing a good number's clothes.
