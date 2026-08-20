# HANDOFF FROM 223a0986, 2026-08-20 AFTERNOON

You hold @980dc16c. Read plans/tree-shaped-log.md TOP TO BOTTOM first -- its
opening sections are Gluck's standing orders and they govern everything --
then this file, then plans/delta-seam-rebased.md, which is the work.

## FIRST FOUR ACTIONS

1. `cd /home/gluck/dev/figaro-qua/layered` -- the work lives here, on
   `feat/layered-cache`. ONE FEATURE BRANCH, ONE WORKTREE, by Gluck's
   instruction. MERGING to main is allowed. PUSHING IS FORBIDDEN WITHOUT HIS
   EXPLICIT APPROVAL, under any circumstances.
2. `figaro cast @980dc16c` -- it points target-aria at you AND studies the
   role. NEVER bind: a bind is a frozen copy with no study.
3. `figla arm --aria @980dc16c --in 20m --about "<DISTINCT TEXT>"` -- distinct
   matters; figla derives its unit name from a truncated prefix and two
   reminders sharing leading words COLLIDE. Gluck asked the last bearer to
   keep a HEARTBEAT armed so work continues without him: re-arm on every fire,
   and stop only when a design question blocks.
4. `figaro set --id <your id> mantra "..."`.

## WHERE THE PROVENANCE IS, BECAUSE YOU WILL NEED IT

    git log --oneline main..HEAD        35 commits from this session alone
    plans/campaign-notes/handoff-*.md   every bearer before you, in order
    plans/delta-seam-rebased.md         the work, with every measurement dated
    plans/tree-shaped-log.md            the standing orders and the campaign's
                                        laws, appended to by each bearer
    plans/delta-seam.md                 the ORIGINAL design (base 34d6e9e0 +
                                        amendment a6682e63)
    fig form @980dc16c show             the role board; `current-work` is the
                                        state, `vocabulary`, `deletion-default`
                                        and `comms-and-complexity` are Gluck's
                                        rules in his own words

COMMIT MESSAGES ARE THE PRIMARY RECORD. Every one of the 35 says what was
measured, what was refuted and what it cost. When a change looks arbitrary,
`git log -S<identifier>` will find the commit that made it and the paragraph
that justified it.

SIBLING ARIAS, ALIVE AND USEFUL:
  - 87ab658e -- typed form values on feat/nested-form-values. It rebases onto
    your branch on a 25-minute heartbeat and will tell you before it touches
    internal/store/form.go. It is careful and it answers from the code.
  - dfdfae6a -- my predecessor. Knows the delta seam's history.

## THE STATE: SECTIONS 1 THROUGH 4 ARE DONE

    1. CATCH-UP          all four providers on provider.CatchUp.
                         ProjectIncrementally, IncrementalProjection,
                         ProjectionConfig, ProjectionStats, lookupCached and
                         EncodedMessages are DELETED; projection.go is gone.
    2. AT RECORD TIME    the fig IR write path translates each entry as it
                         lands, into the channels the aria already has, via
                         the injected store.TranslatorEncoder. FIG IR ENTRY
                         FIRST (Gluck's ruling): a translation that fails is
                         missing, and the catch-up rebuilds it.
    3. asm               (a) providers no longer own the log -- the five-line
                         append ritual, deferredAppendLog and the PREDICTED LT
                         are deleted. (c) all four providers hand over their
                         partial on cancellation, with their native payload.
                         (b) asm still exists: the quadratic that justified
                         deleting it is gone (36 MB -> 285 KB at 1,024 deltas)
                         and what remains is 320 B and 4 allocs per frame, so
                         the deletion is now a TIDINESS change and should be
                         argued as one.
    4. CHANNEL LT        the translator channel is addressed by its OWN LT.
                         transKey, xwalLog.ReadFrom's binary search and
                         readPage's filter all moved off FigaroLT, which is a
                         foreign key.

    5. CHUNKED STREAMING is NOT STARTED. It is the next section: behind a
       setting, ideally including the SDK provider, and GLUCK REQUIRES THE API
       AND SDK GUIDANCE VALIDATED AGAINST THE DOCS (use the brave skill)
       rather than assumed.

## WHAT IS OPEN FOR GLUCK, NOTHING BUILT PAST IT

  - MERGE AND INSTALL. The branch is 35 commits ahead of main and NOTHING IS
    PUSHED. translations-v2 went v2 -> v3, and checkGeneration REFUSES A STORE
    WRITTEN BY A NEWER BUILD -- so a binary from this branch opening the live
    store locks out the installed release until it is upgraded. Not
    corruption; a door that swings one way. Every probe here runs on a COPY.
  - WHETHER TO SWEEP EXISTING ARIAS onto the raw anthropic provider.
    ~/.config/figaro/outfits/house.toml now sets use_official_sdk = false,
    which governs NEW arias only; existing ones carry the old value on their
    boards. The raw provider is ~200x cheaper per send (45-66us / 6 allocs
    against 11.2ms / 73,008 at 1,000 messages).
  - THE SYSTEM PROMPT IS NOT IN THE HISTORY, and he wants it to be. It is
    built from a LIVE snapshot per send and stored nowhere, so a credo edit
    rewrites the system prompt of every past conversation and a replay cannot
    reproduce the request. Written up at the end of delta-seam-rebased.md.
  - COPILOT-MESSAGES now runs on the hand-rolled anthropic provider. Retiring
    anthropicsdk entirely would cost eager/fine-grained tool input streaming,
    which is the ONE capability the raw provider lacks -- and which the
    copilot endpoint rejects anyway. plans/campaign-notes/
    anthropic-sdk-serialization.md has the whole comparison.

## THE INSTRUMENTS, AND USE THEM BEFORE YOU BELIEVE ANYTHING

    scripts/live/onappendlive.sh   a REAL daemon, a REAL provider. It caught a
                                   data-losing bug on its first honest run --
                                   two translations at one FigaroLT, where a
                                   warm read serves the first, so the model
                                   would have been shown UNSIGNED text with the
                                   signed original unreachable beneath it. The
                                   unit suite was green throughout.
    scripts/live/README.md         every live script and WHAT IT CAUGHT.
    FIGARO_REAL_STORE=<copy>       the census and inflation probes.
    FIGARO_MIGRATED_STORE=<copy>   the real-store walk. It COUNTS failures now
                                   (5 of 720, known damage) instead of
                                   stopping at the first, and tolerates a
                                   configured number so a SIXTH is seen.

    NEVER POINT A PROBE AT ~/.local/state/figaro/arias. Copy it
    (cp -a --reflink=auto), run, DELETE THE COPY. One session reached 6.6 GB
    of leftovers in /var/tmp.

## THE RULES THAT ARE NOT MINE TO SOFTEN

  - DELETION IS THE DEFAULT. Keeping a layer is what needs permission. Bad
    tests are removed outright. Inline explanations are purged: a comment is a
    claim nobody tests.
  - COMPLEXITY IS NOT SPENT WITHOUT APPROVAL, reported BEFORE it lands, as a
    complexity change with the operation named -- not as a benchmark result.
  - STANDARD TERMINOLOGY, not project slogans. And the boundary Gluck drew
    this session: A CACHE IS DERIVED STATE. The translator log IS a cache and
    saying so is accurate; the fig IR is derivable from nothing and calling it
    one is the error. I renamed the provider surface off the word and he
    REVERSED IT -- read that ruling before you rename anything.
  - ONE UNIFORM LAYER. A regression is HIS concern: raise it with the number
    and the mechanism, never spend it on an exemption.
  - REDUCE THE MUTEXES: 83 at the start of the campaign, 80 now, the store
    holding 29 of them.

## WHAT I GOT WRONG, SO YOU EXPECT IT OF YOURSELF

  - I RENAMED THE VOCABULARY AND WAS REVERSED. I read "figaro calls a log,
    never a cache" as covering every log figaro owns. It covers what is
    canonical. Ask what a rule is FOR before applying it widely.
  - MY OWN BENCHMARK SWEEP RAN FOR FIVE MINUTES DOING NOTHING because it
    included the 50,000-entry arms whose MemLog fixture takes 2m20s to build
    -- a quadratic I had documented myself two hours earlier and then walked
    into.
  - I DEBUGGED A PROVIDER FOR THREE ROUNDS over a failure that was in the TEST
    DOUBLE: I grew a fake's fields and rewrote its methods in one patch, and
    only the fields landed because gofmt had moved the anchor. A fixture that
    silently drops what it is given accuses the code under test.
  - I PROPOSED A CORRECTION PATH (append a corrected entry at an equal
    FigaroLT) THAT MY OWN TEST REFUTED WITHIN THE HOUR. 87ab658e asked whether
    every reader would believe the rule and said it would cost me a grep. The
    grep PASSED it; the test did not. A grep answers "who reads" and cannot
    ask whether a reader agrees with its own restart.
  - I ASSERTED "the fig IR entry is appended by the provider" for a whole
    session before discovering the append was STAGED and the LT PREDICTED. Ten
    test doubles copied the same fiction. A fiction every fake copies is one
    the tests cannot catch.

## THE ARRANGEMENT THAT MATTERS MOST

Every claim I trust from this session rests on a DETERMINISTIC COUNT --
allocations, entries, visits. Every claim I have had to walk back was a
wall-clock ratio. Report the count first and the timing second, and when a
ratio is flattering, check the EXPONENT rather than the improvement: my
strings.Builder fix looked like 126x and the thing that proved it was the
shape change, 16.3x per 4x deltas becoming 4.5x.

And write the test that names the condition for its own rewrite. The
warm/cold divergence test said "if these now AGREE the divergence is closed
and this test should assert equality instead" -- and when the fix landed two
days later, the instrument told its author what to do with it.

Ring true.
