# HANDOFF FROM ede92072, 2026-08-20 EVENING

You hold @980dc16c. Read plans/tree-shaped-log.md TOP TO BOTTOM first, its
opening sections are Gluck's standing orders, then this, then
plans/delta-seam-rebased.md.

## FIRST FOUR ACTIONS

1. `cd /home/gluck/dev/figaro-qua/layered`, the work is on `feat/layered-cache`.
   ONE BRANCH, ONE WORKTREE. **NOTHING IS PUSHED. THE DAEMON IS NOT UPGRADED**
   and Gluck said explicitly not to until he says so.
2. `figaro cast @980dc16c`, never bind.
3. `figla arm --aria @980dc16c --in 20m --about '<DISTINCT TEXT>'`, distinct
   matters, figla derives its unit name from a truncated prefix.
4. Set your mantra.

## WHERE THINGS STAND

71 commits ahead of main, tree clean, full gate green:

    go test ./... -count=1
    go test -race -count=3   store, figaro, angelus, provider/...
    FIGARO_CRASH_TEST=1 TMPDIR=/var/tmp   ← THE TMPDIR IS PART OF THE GATE
    nix build .#default
    six live scripts

GLUCK'S POSITION: he validated in `nix develop .#snapshot`, said he is
"supportive of the changes", and wants to run FINAL TESTS then have me merge.
He does not want the daemon upgraded before that.

## WHAT LANDED THIS SESSION

  SECTION 5   the request body is written as it is sent. DEFAULT ON;
              `system.stream_request_body` is an ordinary board key read per
              send. Byte-identical to json.Marshal (test + fuzz, 850k execs),
              it has to be, because Marshal HTML-escapes inside a RawMessage.
              1,448,136 B → 663 B per send at 10,000 messages.
  A REGRESSION every send on a new aria failed "empty context", section 4 cut
              the translator span by a MAIN-channel LT. Fixed with the
              channel's own fork base (b48fa289).
  THE STUDY   a cursor advances only when a ROW IS WRITTEN. Gluck's design;
  FIX         his own example is the test (delta of 9 across libretto 6..15).
              Covers the board too. THE FIG IR SIDE IS TOLD NOTHING.
  COALESCING  adjacent same-role rows merge AT ASSEMBLY, never in the log.
  asm         KEPT (it feeds emitLive; deleting it removes streaming) and
              RELEASED in finishTurn, where it used to hold a whole reply.
  INSTRUMENTS a citation checker (a comment naming a test is now checked), and
              scripts/lockpaths.sh (which reads take a lock, and where).

## WHAT IS OPEN, AND WHOSE IT IS

  GLUCK   the SDK pre-marshalled bytes, he approved the direction and said
          SAVE IT FOR LAST, maybe post-merge. option.WithRequestBody takes an
          io.Reader; a non-buffer reader costs the SDK its own retries.
  GLUCK   merge, install, and whether to sweep existing arias onto the raw
          provider (the coalescing gap that blocked it is closed now).
  GLUCK   OnAppend.mu, ONE process-wide mutex held across the whole
          derivation and across calls out of the package. Raised, untouched.
  GLUCK   the benchmark: plans/benchmark-plan.md, designed and UNRUN. Split by
          arm duration, spain for scans, this box for 40-60ns point reads.
  OPEN    THE RESTART LAG. Read the next section before touching it.

## THE RESTART LAG, AND WHAT I GOT WRONG ON IT

restartlive.sh reports a one-turn lag after a daemon restart and carries it as
EXPECTED ("0 is the known lag"). It is pre-existing on main. I did not fix it.

I PUBLISHED A MECHANISM THAT WAS WRONG: the stamp reads the libretto, the
libretto folds asynchronously, so the stamp is stale. It reads convincingly.
Two experiments refuted it, 0 of 40 stale stamps in the steady state, and
after a re-attach the libretto is AHEAD of the source (3 vs 4) before the
stamp.

I then found a REAL ordering defect, nothing opened the librettos at aria
load, so the first opener was studyAccessors() inside the send, after the
prompt was already stamped. Two-arm test: range (3,4] when opened first,
(3,3] when not. Fixed in resumeStudies. **AND THE LIVE SYMPTOM PERSISTS.**

    A MECHANISM THAT IS REAL IS NOT THE SAME AS THE MECHANISM THAT PRODUCES
    THE SYMPTOM, and only the live script can tell them apart.

EXCLUDED NOW: the stamp is not stale; the fold is not behind; the store-level
range after a restart is non-empty when librettos are open first; commit-on-
write is not involved. REMAINING SUSPECTS: whether the aria is already loaded
BEFORE the patch in the live sequence (which would mean the libretto was opened
with nothing to seed, pointing at the fold goroutine's subscription), and what
the provider's catch-up does with the first entry after a restart.

## WHAT I GOT WRONG, SO YOU EXPECT IT OF YOURSELF

  - I PUBLISHED A SCARE NUMBER BEFORE TESTING IT. "40% of arias carry a
    malformed-request shape" was a RECORD count; at the wire the affected aria
    sent perfectly alternating messages and succeeded. Retracted the same hour.
  - FOUR HARNESS FAULTS in one investigation, each producing a confident FAIL
    about figaro: `pkill -f <pattern>` MATCHES THE KILLING SHELL'S OWN COMMAND
    LINE and kills it (three tool calls died that way, and I twice reasoned
    from a patch I believed had landed); a stale sink held a fixed port so my
    sends went to a PREVIOUS RUN'S PROCESS; I discarded a send's output in a
    brand-new script one hour after committing the fix for exactly that in
    three others; and a health check consumed req-01 so the assertions read
    the wrong file.
  - A CONSUMING FAKE made a FIXED defect read as still broken. newFakeBoard's
    cursor yields each version once; a test whose subject is whether a range is
    asked for twice cannot use it.
  - I COMMITTED THE SUITE RED, forgetting that a sibling rebases onto this
    branch every twenty minutes. The campaign's own pattern is to assert the
    defect and name the condition for the test's own rewrite.
  - I CLAIMED "80 mutexes" IN THE COMMIT ABOUT LOCK DISCIPLINE without
    checking. It was 81 before and after: an RWMutex became a Mutex.
  - I SLEPT INSIDE A TOOL CALL THREE TIMES, against the one rule figla exists
    to enforce.

## THE THING WORTH CARRYING

Every instrument I trusted today was one that could say WHY it was empty. The
citation checker refuses a run with no citations. callpath refuses a vacuous
one. The crash gate is meaningless on tmpfs and does not say so, which is why
the gate line now carries TMPDIR. And three live scripts printed PASS for a
whole campaign while discarding the output of the sends they depended on.

    AN INSTRUMENT THAT CANNOT DISTINGUISH "NOTHING HAPPENED" FROM "I DID NOT
    LOOK" WILL EVENTUALLY TELL YOU THE SECOND AND MEAN THE FIRST.

Ring true.
