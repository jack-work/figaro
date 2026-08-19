YOU ARE THE INCOMING BEARER of the figaro state-layer role @980dc16c. I am
f3aa1d0b, handing off at ~72% context on Gluck's instruction. He has gone to
bed; his last words were "get a pace on and quit dilly dallying" and "Godspeed".

FIRST FOUR ACTIONS, IN ORDER:
  1. `figaro cast @980dc16c`   (NEVER bind; a bind is a frozen copy with no study)
  2. `figla arm --aria @980dc16c --in 20m --about "<DISTINCT TEXT EACH TIME>"`
     -- arming on the ROLE id works and survives; DISTINCT text matters because
     figla derives its unit name from a truncated prefix and two reminders with
     the same leading words COLLIDE, silently downgrade, and one fires early
     carrying the other's message. Verified tonight.
  3. `figaro set --id <your id> mantra "drive the cache consolidation"`
  4. Read plans/tree-shaped-log.md on feat/layered-cache TOP TO BOTTOM. Its
     first four sections are Gluck's standing orders and they govern everything.

## GLUCK'S CORE GOALS, HIS ORDER, HIS WORDS

  ONE unified tree-shaped in-memory cache which is CANONICAL
  layer and code REDUCTION and consolidation
  memory / system usage / performance
  reliability and correctness wins WHERE THEY MAKE SENSE

Plus: keep it focused, REWRITE rather than patch where a rewrite is cleaner,
test through nix devshells (tmux where a terminal is involved), benchmark what
you change, and TRIM SUBAGENTS WHOSE SCOPE IS CREEPING.

## THE FOUR STANDING ORDERS, ON THE ROLE BOARD -- READ THEM THERE

  single-layer-block    ONE uniform cache layer. A regression is HIS concern:
                        RAISE it, do not engineer around it. HALT if
                        convergence is violated.
  deletion-default      Existing code does not survive in reduced form. RETAINING
                        a partially-obsolete structure needs his EXPLICIT
                        approval; deleting is the default. Bad tests removed,
                        not replaced. Inline explanations purged.
  comms-and-complexity  Standard CS terminology, not project slogans.
                        O(1)->O(n) or O(n)->O(n^2) needs his approval BEFORE it
                        lands. Halt when pending approvals would be prejudged.
  rotation-fact         'nothing has ever rolled' was TRUE OF FORM CHANNELS
                        ONLY. 35 rotations have occurred at 2 MiB.

AND THE ESCALATION RULE, given tonight: ANY LOCK WITH GENUINELY CONCURRENT
CALLERS IS RAISED, NOT WORKED AROUND. Work around only if he is absent AND
reminders are accumulating; document as a follow-up either way.

## WHERE THE TREE IS

  main                 all of tonight's landings, release still 0.27.0
  feat/layered-cache   merged onto main, the plans live here
  feat/delta-seam      merged onto main

LANDED TONIGHT: figwal merged IN-TREE at internal/store/{segment,disk,log,xwal,
crashtest,tree} across seven commits (forest renamed to tree everywhere -- "quit
using forest, its a tree"); the stage-2 memo, the merge join, the seek fix and
THE PROJECTION DELETION; the cast lost-study race fix; openNode unexported; the
segment cache RANGE UNIT (one record's cold read 200 frame reads -> 32,
sequential scan unchanged, and loadMu fell out); the thirty-lock survey; and the
tree cache's Source now runs OUTSIDE c.mu.

BOTH CAMPAIGN BRANCHES WERE MERGED, NOT REBASED, DELIBERATELY: 160 cited hashes
in plans/ and notes, 54 of the last 60 commit messages citing commits, every
gate log naming its tree. A rebase voids all of it -- unverifiable, which is the
state this project ruled inadmissible for gate logs and has enforced since.

## THE DESIGN, AS IT STANDS

THE CACHE IS THE PROJECTION. On-disk bytes and wire bytes are identical EXCEPT
at known offsets where cache_control is stamped; the encoder records those
offsets, the streaming writer stamps mid-stream, no deserialization.
copilot/responses ALREADY passes bytes through untouched -- existence proof; the
other three decode and re-encode every turn.

BORROWING: eager materialization of the whole lineage path, explicitly pinned,
released run by run as the stream passes. Not lazy -- lazy puts a segment read
in the middle of a socket write.

RESIDENCY: forest/tree.Cache is the ONE policy. segment/cache.go is already
re-seated on it; what remains there is a lock-free fast pointer, not a second
cache. THE TENANT USES THE TREE DEGENERATELY -- coordOf returned {From:0,To:1},
now fixed for segments -- and tree.New is still called with src=nil, so
rematerialization is UNREACHABLE FROM THAT TENANT. Installing a Source is the
consolidation's next structural step, and it is why the Source-under-lock fix
had to land first.

THE LOCKS: 82 in non-test code, ~30 in internal/store, all classified in
plans/store-locks.md. 10 of 30 answer NONE to "what invariant spans this
critical section that could not be published as one immutable value". 17 of 30
CALL OUT from inside the critical section. THE ROOT CAUSE: figwal's locking is
DEFENSIVE CONCURRENCY WRITTEN FOR AN UNKNOWN CALLER, and it stopped being
unknown when it came in-tree. Gluck: the IR and translator writes are ALREADY
serialized by the main agent loop. So STATE THE CONTRACT, ASSERT IT WHERE IT CAN
FAIL, DELETE THE LOCKS THAT EXISTED ONLY FOR ITS ABSENCE.

## IN FLIGHT

  fd15d2a0  THE STANDING EXECUTOR. Excellent, self-correcting, corrects me
            unprompted. Just landed the Source-outside-the-lock fix. Next: the
            contract-and-delete work, starting with MemFormLog under runBatch.
  6ec565b5  STOOD DOWN tonight (its measurement no longer gates a decision).
  9ed3f561  near end of context; its harness is at scripts/callpath.
  c64cacf2  released, done -- six clean commits, the figwal merge.

## FOLLOW-UPS LOGGED, NOT LOST

  'compact' is overloaded three ways and the only meaning a reader assumes is
  the one this system NEVER DOES. Rename to evictWindow. When convenient.
  The store must carry its own bounded residency defaults; today boundedness is
  a property of ONE CALL SITE in internal/cli.
  Two open measurements: OPEN-ns-rebank (moved to spain, needs fresh baselines
  there -- nanoseconds do not travel between machines) and the decoded-struct
  retention multiplier.

## THE STANDARDS, NOT NEGOTIABLE

Gate logs stamp BEGIN and END with head, dirty count, diff digest, toolchain,
loadavg, and they MUST AGREE. Assert the ARTIFACT, never a status field, and
NEVER `&& echo OK` after a pipe -- I got that wrong tonight and it is in a
commit message. Canary every test; A PASSING CANARY IS A FINDING. Count rather
than time where the question is "how many times". Corrections go in NEW
paragraphs beside what was read. The claim is written AFTER the verification.
Cross-aria patches are apply-checked against the RECIPIENT'S head, and the
rehearsal names the head it was rehearsed at.

## WHAT I GOT WRONG, SO YOU EXPECT IT OF YOURSELF

Corrected five times by workers and three times by Gluck. I upheld a
measurement refusal three times without asking what the measurement was MADE OF.
I under-counted a consolidation threefold by costing each layer as what it would
BECOME. I read the absence of a lock as the absence of ordering. I called
window eviction "compaction" in a system that never compacts. I ran a build
through a pipe and read head's exit status.

RULE FAST ENOUGH TO BE WRONG EARLY. If a turn passes with nobody correcting
you, be suspicious of the arrangement rather than pleased with it.

Ring true.
