# PRE-REGISTRATION: THE FORK-SEAM SHARING MEASUREMENT

Aria 6ec565b5, 2026-08-19, registered BEFORE the instrument was run and before
it compiled. Branch meas/fork-seam-sharing off feat/layered-cache@4139d2f2.

## THE QUESTION

Not "how much does one projection retain" (a TOTAL, already answered at 1.19x
by b9925789) but HOW MUCH OF A FORK'S RESIDENT PREFIX IS SHARED WITH ITS
PARENT -- what a fork's residency costs that the parent has ALREADY PAID.

## THE METHOD, WHICH IS 9ed3f561'S, TURNED SIDEWAYS

The same keep-versus-drop heap delta, on ONE fixture, twice, ACROSS THE FORK
SEAM instead of across one object's lifetime:

    RUN A (PARENT ALIVE)    heap with child alive, minus heap with child
                            dropped, while the parent stays reachable.
                            = THE CHILD'S MARGINAL COST.
    RUN B (PARENT DROPPED)  the same delta with the parent already
                            unreachable. = THE CHILD'S COST AS SOLE OWNER.

    SHARED = B - A. It is what the parent had already paid for.

Bytes, not time: it does not need a quiet box or the bench lock.

## WHAT I PREDICT, WITH THE FALSIFIER NAMED

The seam's own comment (cached_log.go, newSeededLog) says: "opening a fork
re-decodes the shared prefix and mints every string the parent already holds,
and A SHALLOW COPY SHARES ALL OF THEM. So the child pays slice headers where
it used to pay a decode."

    PREDICTION 1. A is SMALL and roughly len(seed) x sizeof(Entry[T]) --
    slice headers and struct fields, not payload bytes.
    PREDICTION 2. B is LARGE, of the order of the encoded payload bytes,
    because once the parent is gone the child is the only holder of the very
    same strings and they stay alive.
    PREDICTION 3. Therefore SHARED = B - A is most of the fixture's bytes,
    and the ratio A/B is small.

    FALSIFIER: if A is within 20% of B, THE SEED IS NOT SHARED and the child
    is paying for its own copy of the prefix. That falsifies the comment
    quoted above, not merely my expectation, and it is the outcome that would
    make the fork seam a place where a tree buys memory rather than structure.

## THE CANARY, AND IT MUST PRODUCE THE FAILURE MODE

An instrument that reports "the prefix is shared" must be shown capable of
reporting "the prefix is NOT shared". newSeededLog DEGRADES TO A DECODE on
every doubt -- empty seed, non-ascending seed, a seam probe that does not
match. So the canary drives that degradation deliberately (a seed the probe
rejects) and asserts that A RISES TO MEET B. If A does not move, the
instrument is measuring a total again and is the wrong instrument -- exactly
503b9650's condition.

## WHAT THIS CANNOT SAY, REGISTERED NOW SO IT CANNOT BE FORGOTTEN LATER

  - IT IS A FLOOR. The fixture holds raw byte payloads. Typed provider SDK
    structs are larger and unmeasured, as b9925789 recorded for the same
    reason.
  - IT MEASURES THE SEAM, NOT THE BACKEND'S LIFECYCLE. XwalBackend memoises
    one handle per aria (b.open[id]), so dropping a caller's reference frees
    nothing while the backend holds it. The instrument therefore builds the
    two caches directly, which is the mechanism under test; a whole-backend
    measurement is a different experiment and is not this one.
  - IT CANNOT SAY WHICH FIELD HOLDS THE BYTES. A heap delta answers "how much
    does holding it cost", not "what holds it".
  - IF SHARING IS ALREADY FREE, THAT IS NOT AN ARGUMENT AGAINST THE TREE. It
    would mean the tree buys correctness and structure rather than memory,
    which is a different justification and must be written down as that one.
    NO VERDICT EITHER WAY: the numbers go up, the choice is Gluck's.
