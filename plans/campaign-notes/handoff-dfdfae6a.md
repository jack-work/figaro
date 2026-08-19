# HANDOFF FROM dfdfae6a, 2026-08-19 EVENING

You hold @980dc16c. Read plans/tree-shaped-log.md top to bottom first (its
opening sections are Gluck's standing orders), then this file, then
plans/delta-seam-rebased.md, which is the work.

## FIRST FOUR ACTIONS

1. `cd /home/gluck/dev/figaro-qua/layered` -- THE WORK LIVES HERE NOW, on
   `feat/layered-cache`, by Gluck's instruction: one feature branch, one
   worktree. I developed on main earlier today; he saw it, endorsed the
   remedy, and the branch was fast-forwarded to main's head. MERGING to main
   is allowed. PUSHING IS FORBIDDEN WITHOUT HIS EXPLICIT APPROVAL.
2. `figaro cast @980dc16c` -- never bind; a bind is a frozen copy with no
   study.
3. `figla arm --aria @980dc16c --in 25m --about "<DISTINCT TEXT>"` -- distinct
   matters, figla derives its unit name from a truncated prefix and two
   reminders sharing leading words collide.
4. `figaro set --id <your id> mantra "..."`.

## THE CAPITAL PRIORITY, IN GLUCK'S WORDS

ProjectIncrementally is removed; `asm` goes with it; the wire is served from
the log interface directly; every log's bytes get ONE canonical cache-backed
log implementation. The delta-seam work "SHOULD NEVER HAVE BEEN DROPPED" --
it was, `feat/delta-seam` is not an ancestor of main, and working it into the
running implementation is the job.

plans/delta-seam-rebased.md holds the order of execution, what moved beneath
the original plan, and every number taken today. START THERE, AT SECTION 1.

## WHAT LANDED TODAY

    Q3'S THIRD TENANT     the composed UI IR is on tree.Cache, keyed by the
                          turn's OPENING LT. UIBudget deleted entire. A
                          fork's composed prefix is its ancestor's runs; the
                          donation, both per-server composers and the
                          TurnSource type are gone.
    fig show              composes NOTHING -- it pages aria.page and renders.
                          Byte-identical to the old output on 33 of 48
                          real-store checks; the rest are two understood
                          classes (one of them a fix).
    Q2                    ANSWERED WITHOUT THE STRUCTURE. The idle sweep was
                          quadratic by QUESTION, not by shape: 180 ms -> 294
                          us at R=4096, one pass per owner, no index, nothing
                          written on the read path.
    THE ASSEMBLER         anthropic and openaichat splice stored rows
                          verbatim. Allocations at 50,000 messages: 600,064
                          -> 88, CONSTANT IN CONVERSATION LENGTH.

## WHAT IS OPEN, IN ORDER

1. THE CATCH-UP. One `provider.CatchUp` replacing ProjectIncrementally and
   the four per-provider `catchUp` wrappers: walk the fig IR from THE LOG'S
   OWN watermark (the translator log's tail FigaroLT), encode what is
   missing, append, return stats. No State, Append, Initial, Previous or
   generic parameter -- those five build the representation being deleted.
2. ROWS AT WRITE TIME, in `store.irDoor.write`. It is the only site that can:
   single write path, it MINTS records nobody else knows about (tool-close,
   late-result note), and it REWRITES the payload before it lands. Two open
   questions are named in the plan: keeping provider knowledge out of the
   store, and whether figwal can land a record and its rows in one frame.
3. asm, torn out. The union and falsifiers in plans/delta-seam.md stand.
4. The translator log addressed by its CHANNEL LT. Four of the five sites
   that index it by fig IR LT are artifacts; the fifth is a stamp the
   alignment check reads.
5. Chunked streaming, LAST, behind a setting, ideally including the SDK
   provider -- and Gluck says to VALIDATE the API and SDK guidance against
   the docs (brave) rather than assume it.

AND THE OLDER QUEUE, not dropped: the segment scan's 8 bytes (+31% on a
sequential scan; the cure is a nil-Keyer positional mode), lazy boot for
forks (compose only above the base), sibling 041454f1 on
feat/nested-form-values needing a rebase, and the standing mutex goal (83 ->
80; the store still holds most of them).

## THINGS I GOT WRONG TODAY, SO YOU EXPECT THEM OF YOURSELF

  - I PROPOSED A TWO-CURSOR MERGE AND GLUCK ASKED WHY IT WAS NECESSARY. It
    was not. With rows written at record time there is no join to perform; I
    was carrying the old shape forward. Withdrawn in writing.
  - I REPORTED A DROPPED `is_error` AS A POSSIBLE DEFECT before asking what
    its VALUE was. Every dropped one was `false`, which omitempty drops and
    which means nothing. One more measurement would have saved the alarm.
  - MY FIRST `evictOlderThan` WAS ITSELF QUADRATIC, and only the EXPONENT
    caught it: 109x for 16x the runs where linear predicts 16x. It was
    already 31x faster than before, which is the size of improvement that
    ends an investigation.
  - TWO OF THREE CANARIES PASSED ON FIRST APPLICATION and neither meant what
    it looked like: one fixture left the straddling turn in the staging slot,
    and the truncation repair had NO witness in the whole suite because every
    fixture composer returns whole turns by construction.
  - I DEVELOPED ON MAIN because my handoff said main was the only line. Tell
    Gluck what you are doing to his branches before you do it, not after.

## THE STANDING RULES THAT ARE NOT MINE TO SOFTEN

FIGARO CALLS A LOG, NEVER A CACHE -- the cache is an implementation detail
contained entirely within the canonical log implementation. It is on the role
form as `vocabulary`, in his words, and it applies to identifiers, comments,
plans and chat alike.

A PASSING CANARY IS A FINDING, and a passing canary WITH AN EXPLANATION
ATTACHED is a finding that has been talked out of existence. Count rather than
time where the question is "how many times". Report the deterministic quantity
ahead of the timing. Two benchmarks in ONE binary with an in-run control;
interleaving is not counterbalancing. A single-shot heap delta is an
unrepeated measurement. And the claim is written AFTER the verification --
which I broke once today and recorded rather than smoothed.
