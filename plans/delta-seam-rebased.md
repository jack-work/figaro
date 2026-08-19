# THE DELTA SEAM, REBASED ONTO A MAIN THAT MOVED (dfdfae6a, 2026-08-19)

Gluck, today, twice: "the guidance for the delta-seam work SHOULD NEVER HAVE
BEEN DROPPED and is essential to work into the running implementation -- not
parked on a branch"; and "that entire delta-seam work was never supposed to be
dropped and it seems like it was. It is a critical requirement."

He is right and it was dropped. `feat/delta-seam` is NOT an ancestor of main.
The tree campaign paused stage 2, recorded the pause as "HALTED and LANDED,
not reverted", and then main was rebuilt around the tree without it.

THIS FILE DOES NOT RESTATE plans/delta-seam.md. That plan (base 34d6e9e0 +
amendment a6682e63, on feat/delta-seam) is still the design. This is what has
CHANGED UNDER IT, what the measurements now say, and the order of execution.

## WHAT MOVED BENEATH THE PLAN

    cachedLog                 DELETED. Both decoded channels and the composed
                              UI IR are tenants of internal/store/tree.
    THE ONE DOOR              store.irDoor is the single write path into the
                              fig IR. Every append -- agent, provider, daemon,
                              repair, study -- passes through irDoor.write.
    THE IDLE SWEEP            one pass per owner, not R^2.
    THE CLI                   composes nothing; `fig show` pages the api.
    THE ASSEMBLER             splices stored rows verbatim (anthropic,
                              openaichat; responses always did).

The plan's Part II sentence "cachedLog IS JUST Log" is DONE. Its Part I and
Part III are not.

## THE MEASUREMENTS TAKEN TODAY, WHICH THE PLAN DID NOT HAVE

Against a reflink copy of the real store, 198 arias, 75,200 anthropic rows:

    ROWS ARE WIRE-FINAL AND WERE BEING REBUILT ANYWAY. Zero rows survived the
    old assembler byte-identically; 54.2% differed only in key order; the
    whole of the remaining difference was `.content[].is_error` dropped by
    omitempty on 38,827 rows, EVERY ONE OF THEM FALSE. Semantically faithful,
    byte-wise wasteful. Splicing verbatim took assembly allocations from
    600,064 to 88 at 50,000 messages -- CONSTANT IN CONVERSATION LENGTH.

    ONE ROW PER RECORD, ONE GENERATION. Zero records carry more than one row;
    the entire store holds ONE fingerprint, "anthropic-sdk/tag/v1". So a read
    that walks the log in order cannot double a message today, and the
    fingerprint-in-channel-identity rule has one generation to migrate.

    THE UNSTAMPED THIRD. 212 of 672 non-empty arias carry NO turn ids on their
    opening records, and 1,687 of 8,533 openers are unstamped. Anything that
    numbers turns from a bracket renumbers those arias.

## WHAT ProjectIncrementally IS, ENUMERATED BEFORE DELETING ANYTHING

Six jobs, and five exist ONLY because it encodes LATE:

    1. BACKFILL      a record with no row is Encoded and the row appended.
                     A WRITE, on the read path.
    2. ASSEMBLE      config.Append builds the messages array. The only job
                     that is genuinely a read.
    3. THE BOARD     carries snap/snapAt/lastForm, folding form patches so
                     Encode can render the board AS IT STOOD AT THAT RECORD.
    4. THE STUDIES   lastStudy[fid] per observed form, with the rule that a
                     study window may only close on a record that can carry
                     the block.
    5. THE WATERMARK Previous + fingerprint + prefix-length validation, with a
                     cold-walk fallback.
    6. THE MEMO      IncrementalProjection: State, Form, Entries, LastLT and
                     five version fields -- a second in-memory cache that also
                     PINS bytes the log's window believes it evicted.

    JOBS 3, 4, 5 AND 6 HAVE NO SUCCESSOR. They are not features; they are the
    cost of asking "what did the board look like then?". The writer already
    knows, because it IS then.

## GLUCK'S RULING ON THE SHAPE (2026-08-19)

  - ProjectIncrementally STILL FUNCTIONS, but it WRITES DIRECTLY TO THE LOG
    and keeps no representation of its own.
  - It is NOT called ProjectIncrementally. It is a CATCH-UP, and it is merged
    with the four per-provider `catchUp` wrappers that exist today
    (anthropic, anthropicsdk, copilot/responses, openaichat).
  - Then the provider -- no projection, no state -- ASKS THE LOG FOR THE FULL
    HISTORY and marshals it to the wire, streaming where it can.
  - A MISSING ROW IS AN ERROR, NOT A RE-ENCODE. On this path "degrade to a
    miss" means sending the model a different conversation than the one on
    disk.
  - FIGARO CALLS A LOG, NEVER A CACHE. The cache is an implementation detail
    contained entirely within the canonical log implementation. (On the role
    form as `vocabulary`.)
  - The translator log is ADDRESSED BY ITS OWN CHANNEL LT, not by the main
    figaro LT.

## THE ORDER OF EXECUTION

### 1. CATCH-UP (in progress)

One `provider.CatchUp(cfg) (Stats, error)` replacing ProjectIncrementally and
the four wrappers. It walks the fig IR from the LOG'S OWN WATERMARK -- the
translator log's tail FigaroLT, not an in-memory Previous -- encodes what is
missing, and appends. It returns stats. NO State, NO Append, NO Initial, NO
Previous, NO generic type parameter: those five exist only to build the
representation that is being deleted.

The read that replaces `projection.State`: the rows themselves, in order.
Anthropic and openaichat already splice them verbatim.

### 2. ROWS AT WRITE TIME

The row is written by `store.irDoor.write`, which is the ONLY site that can:

  - it is the single write path (XwalBackend.Open returns nothing else, and
    GuardIR wraps every other fig IR log in it);
  - IT MINTS RECORDS NOBODY ELSE KNOWS ABOUT -- the tool-close message and
    the late-result system note -- so a row written by any caller upstream
    would have no row for exactly the records that must reach the wire
    well-formed;
  - IT REWRITES THE PAYLOAD BEFORE IT LANDS (dropping unmatched tool_result
    blocks, completing partial sets in place), so the wire-final encoding is
    a function of the POST-REPAIR payload, which exists nowhere else.

TWO THINGS THIS FORCES, and they are the open design questions:

    THE STORE MUST NOT LEARN ABOUT PROVIDERS. The encoder arrives as an
    injected writer; the store keeps no provider knowledge.
    A HOOK MAKES THE ROW A SECOND WRITE. Rule 6 says a missing row is an
    error, so the record and its rows want to land together. Whether figwal
    can commit two channels in one frame decides the shape; if it cannot, the
    door owns a repair on open, which reintroduces the lazy encode this
    deletes.

With the fingerprint in the channel identity (`translations/<provider>/<fp>`),
a bump starts a FRESH CHANNEL: there is never a stale row to serve, and the
catch-up's fingerprint check disappears with the staleness it was checking.

### 3. asm, TORN OUT (Gluck's standing ruling)

Providers emit fig IR DELTAS and one MESSAGE carrying its natives, in order,
and never learn an LT or a turn. The fig IR side holds both appends because
only it has the LT, which makes the ordering invariant A SHAPE THAT CANNOT BE
VIOLATED rather than a rule needing a test. The interrupt becomes a
provider-owned premature close. See plans/delta-seam.md for the union and the
pre-registered falsifiers -- they still stand.

### 4. THE TRANSLATOR LOG ADDRESSED BY ITS CHANNEL LT

Five sites index it by fig IR LT today. FOUR ARE ARTIFACTS:

    lookupCached -> Log.Lookup(entry.LT)   one random lookup per record, per
                                           request. Dies with the projection.
    xwalLog.ReadFrom(figaroLT)             a BINARY SEARCH with a ReadAt per
                                           probe, because a side channel's
                                           main-LT is not its channel-LT. The
                                           main channel is identity and O(1).
    treeLog.key = FigaroLT                 the residency index keyed on a
                                           FOREIGN KEY: not unique (a turn's
                                           rows share one), so a coordinate
                                           holds several units, seedOnAppend
                                           is off, and a run can never be
                                           DENSE -- the arithmetic fast path
                                           is structurally unavailable.
    readPage filters by FigaroLT           the channel paged in another log's
                                           coordinates.

The fifth is the stamp itself (projection.go:201), and it stays -- as a FIELD
THE ALIGNMENT CHECK READS, not as the address. THE TWO-CURSOR MERGE I
PROPOSED IS WITHDRAWN: with rows written at record time, the read is one
sequential read of one log and there is no join to perform.

### 5. CHUNKED STREAMING (last)

Part III of plans/delta-seam.md stands: the provider decides its own
transport, anthropic streams the body from the iterator, responses cannot
(websocket.JSON.Send marshals a frame). Gluck adds: IDEALLY THE SDK PROVIDER
TOO, BEHIND A SETTING, and the API/SDK guidance is to be VALIDATED AGAINST
THE DOCS rather than assumed. The verbatim splice was its prerequisite and is
done.

## WHAT IS DONE ALREADY

  - anthropic and openaichat splice stored rows verbatim; responses always
    did; anthropicsdk is NOT converted (the vendor SDK owns marshalling the
    request, which is a delta-seam question, not an assembler one).
  - rows_roundtrip_probe_test.go and the rows-per-record probe are permanent,
    env-gated instruments against a real store copy.

# RAISE: anthropicsdk's CONVERSION IS AN O(history) UNMARSHAL PER SEND
# (223a0986, 2026-08-19)

NOT DONE. This is a complexity change reported before it lands, under rule 3,
with the operation named. openaichat and copilot/responses ARE converted
(37d7fe94); anthropicsdk is the one that does not fit, and ProjectIncrementally
cannot be deleted until it is answered.

## THE MECHANISM

The three converted providers hand STORED BYTES to the wire. Their
whole-history read is slice headers over payloads the log already owns.

anthropicsdk hands `[]anthropic.MessageParam` VALUES to the vendor SDK, which
owns marshalling the request. A read that keeps no representation must
therefore UNMARSHAL THE ENTIRE CONVERSATION ON EVERY SEND, where the deleted
memo parsed each row ONCE, when it first arrived.

    OPERATION: assembling a request body on anthropicsdk goes from O(new
    messages) parsed to O(conversation length) parsed, every turn.

## WHAT IT COSTS, MEASURED (BenchmarkParseRowsToMessageParams, 5800X, bench
## lock held, n=50 x 3 repeats). ALLOCATIONS FIRST, THEY ARE DETERMINISTIC:

    messages   allocs/op     B/op        ns/op
       1,000      73,001     5.87 MB     10.5-11.3 ms
      10,000     730,003    58.7 MB      108-113 ms
      50,000   3,650,002   293.6 MB      486-494 ms

73 allocations and 5.87 KB PER MESSAGE, exactly linear at three sizes.

AGAINST THE CONVERTED PROVIDERS' READ (BenchmarkRows, same box, same lock):

    messages   allocs/op     B/op        ns/op
       1,000           2     32.8 KB     5.8-6.9 us
      10,000           2    327.7 KB     77-112 us

TWO ALLOCATIONS, CONSTANT IN n, and 32.768 BYTES PER MESSAGE -- one slice
header plus one uint64. The payloads are shared with the log, never copied.
So the SDK path would pay ~36,000x the allocations of the raw path for the
same conversation.

## THE OPTIONS, WITHOUT A RECOMMENDATION

  (a) CONVERT AS-IS and pay the numbers above per send. At a 2,556-record aria
      (the largest in the real store) that is ~27 ms and ~15 MB per turn.
  (b) STOP HANDING THE SDK TYPED MESSAGES: if the vendor client can be given a
      pre-marshalled request body, the SDK provider splices stored rows
      verbatim like anthropic does and the question disappears. UNVERIFIED --
      Gluck's standing instruction is that SDK guidance is validated against
      the docs (brave) rather than assumed, and it has not been.
  (c) RETIRE anthropicsdk. It is reachable two ways: `anthropic` with
      knobs.UseOfficialSDK, and copilot's `copilot-messages`, which wraps it
      unconditionally. So this is a deletion with a live caller, not a dead
      branch.

WHAT IS BLOCKED UNTIL THIS IS ANSWERED: ProjectIncrementally,
IncrementalProjection, ProjectionConfig, EncodedMessages, AppendEncodedMessage
and lookupCached all still stand for this ONE provider. Nothing has been built
past any of the three answers.

## AND A FIXTURE FACT FOUND WHILE MEASURING, NOT A DEFECT IN THE CODE

`store.MemLog.Append` copies the entries slice AND REBUILDS `byFigaroLT`
ENTIRELY on every append, so BUILDING a MemLog is quadratic with a map rebuild
per element. Measured on this box: 121 ms at 1,000 entries, 5.01 s at 10,000,
2 m 20 s at 50,000.

EVERY PROVIDER BENCHMARK THAT BUILDS A 50,000-ENTRY MemLog PAYS THAT PER
FIXTURE, untimed but in wall clock, and anthropic's Cold arm rebuilds one
inside its b.N loop. Those arms are effectively unrunnable at 50,000, which
means the invariant they name has not been checked at that size in some time.
BenchmarkRows therefore stops at 10,000 and says why. MemLog is a test fixture
except as an emergency fallback in agent.go, so this is reported, not fixed.

# THE RENAME THAT WAS MADE AND UNMADE WITHIN THE HOUR (223a0986, 2026-08-19)

I renamed the provider surface off the word cache -- CacheOpen -> RowsOpen,
cacheFor -> rowsFor, the field to rows, ClearStaleTranslationCache ->
ClearStaleRows -- reading the vocabulary rule as covering it. GLUCK REVERSED
IT, and his reason is the one that decides the boundary the rule was always
drawing:

    "the translator/provider/assistant 'cache' is really a cache, since its
     all derived state, so its fine as is."

THE TEST IS DERIVABILITY, NOT LAYER. A translator log holds state that can be
rebuilt from the fig IR, so it IS a cache and saying so is accurate. The fig
IR is not derivable from anything, so calling IT a cache is the error the rule
forbids. Reverted whole (55df12cc reverted); the names are as they were, and
provider.AssistantCache and figaro.commitAssistantCache stay exactly as they
are with them.

# SECTION 2 PRELIMINARIES: WHAT THE SUBSTRATE ACTUALLY OFFERS (223a0986)

Read in figwal and the store rather than assumed, prompted by dfdfae6a's
caution to check ORDERING before designing around ATOMICITY.

  1. THERE IS NO MULTI-CHANNEL TRANSACTION. `XWAL.AppendMain` and
     `XWAL.Append` each take one channel's lock and write one frame to that
     channel's own log. Nothing in the surface commits two channels together,
     so ATOMICITY IS NOT AVAILABLE and ordering is the only lever.

  2. A ROW MAY BE WRITTEN BEFORE ITS RECORD EXISTS, BY THE SUBSTRATE'S OWN
     CONTRACT. `XWAL.Append`'s mainLT "must be >= the channel's last
     referenced main LT (IT MAY EXCEED THE CURRENT MAIN TAIL, TO SUPPORT
     CATCH-UP)". So the row-first ordering is legal, and it is the ordering
     whose failure mode is survivable: an orphan row is trimmable, an orphan
     record is fatal under "a missing row is an error".

  3. AND THE THING THAT COMPLICATES IT, WHICH IS NOT THE LT. The next main LT
     is predictable (`LastIndex()+1` under the writer's lock, and turn.go
     already predicts it and asserts the prediction). THE STAMP IS NOT. On the
     main channel `xwalLog.Append` calls `AppendCursors` with the store's
     observed cursors, and then READS THE RECORD BACK to learn
     FormChannelVersion and StudyVersions -- "fields the store stamps and the
     caller cannot know". CatchUp encodes FROM those two fields: they decide
     which form patches and which study blocks the row renders.

     SO A ROW CANNOT BE ENCODED BEFORE ITS RECORD LANDS without recomputing
     the stamp outside the append that owns it -- a second reader of state the
     append reads under a lock, which is a race unless the door's lock covers
     both. That is the real design question in section 2, and it is sharper
     than "can figwal commit two channels in one frame": IT CAN'T, AND THE
     ENCODING DEPENDS ON A VALUE THAT ONLY EXISTS AFTER THE WRITE.

NOTHING BUILT PAST THIS. The three shapes it leaves are: row-first with the
stamp hoisted into the door (one lock, one computation, passed to both
writes); record-first with a repair on open (the lazy encode this work
deletes, reintroduced); or the stamp made an input rather than an output of
the append, which is a figwal surface change.

# SECTION 2, THE SHAPE: ROW FIRST, IDENTITY FROM CONTENT, STAMP AS AN INPUT
# (223a0986 with 87ab658e, 2026-08-19)

Gluck ruled the two appends are not to be designed around: write the ROW
FIRST, at the LT the record is about to take, then the record. Ordering
substitutes for atomicity because THE TWO ORPHANS ARE NOT EQUALLY BAD -- an
orphan record is fatal under "a missing row is an error", an orphan row is
not.

## THE HAZARD, AND WHY IT IS NOT A GAP BUT A LIE

A crash between the two writes leaves a row at an LT THE NEXT APPEND WILL HAND
TO A DIFFERENT MESSAGE, because the next main LT is PREDICTED, NOT RESERVED:
disk.Log.LastIndex() derives from segment contents and no counter is
persisted. The orphan then reads as a legitimate translation of a record it
does not describe, and the model is shown a message that was never in the
conversation.

TWO WAYS OUT WERE CHECKED AND ARE ABSENT: there is no durable reservation, and
there is NO TAIL TRUNCATION anywhere in the substrate (disk.Log has
TruncateFront only; XWAL.Clear takes the whole channel).

## THE ANSWER, WHICH IS 87ab658e'S D10 APPLIED TO A DIFFERENT LOG

    IDENTITY FROM CONTENT, NOT FROM POSITION. The row carries the CONTENT HASH
    of the record it translates. LT reuse then cannot produce a
    legitimate-looking orphan: the row either matches the record at that
    position or it does not.

Their words, from the form campaign where the same shape was ruled: outfit
identity was the hash of the PATCH, which made identity a function of the path
taken rather than the state reached; Gluck's ruling was to hash the RESULTING
VALUE. It does not buy atomicity. IT BUYS DETECTABILITY, which is the thing
actually missing -- the failure's problem was never its frequency.

THE SUBSTRATE ALREADY HAS THE MECHANISM: segment.ValueHash is truncated
SHA-256 over CANONICAL JSON (keys sorted, 16 hex chars), and the JSONL codec
already writes a `_hash` sidecar per record for integrity. xwal.Record does
not surface it, so the door hashes the payload it is about to write -- one
hash per record, on the write path, payload already in hand.

    AND THE CHECK IS O(1), NOT O(history). The door writes row-then-record
    under one lock, so a crash leaves AT MOST ONE orphan: the newest row. Only
    that one is verified, once per open.

    REMOVAL WITHOUT A TRUNCATION API -- PROPOSED, AND REFUTED BY ITS OWN
    TEST. See the correction below: appending at an equal FigaroLT is legal
    and durable, but the live handle cannot see the second row.

## AND THE STAMP BECOMES AN INPUT, WHICH PAYS FOR ITSELF

The row's encoding is a function of FormChannelVersion and StudyVersions.
Today xwalLog.Append computes the cursors inside the append and then READS THE
RECORD BACK to recover them -- "fields the store stamps and the caller cannot
know".

87ab658e names it as the defect they had already deleted once: a value's type
erased at the storage boundary and reconstructed by parsing on the way out.
"The fix was not to parse faster, it was to stop discarding what the writer
already knew."

So the door computes the stamp, encodes the row against it, and PASSES IT INTO
the append. The record then carries exactly the stamp its row was encoded
against, and no form patch can land in between -- the race is closed by the
shape rather than by a lock. IT ALSO DELETES ONE ReadAt PER APPEND, so the
hoist pays for itself before the row work benefits.

## WHAT NEEDS GLUCK BEFORE IT LANDS

  1. A FIELD ON EVERY STORED ROW (the record's content hash). Not a data
     structure, but it changes what is written, and EVERY EXISTING ROW LACKS
     IT. Rows are DERIVED STATE -- his own ruling today -- so the migration is
     a clear and a re-catch-up, which costs one re-encode per aria, once.
  2. WHETHER THE DOOR MAY HOLD AN ENCODER AT ALL. "The store must not learn
     about providers" is the plan's own constraint; the encoder arrives as an
     injected writer, and the shape of that injection is the open question the
     original plan named.

## CORRECTION, SAME HOUR, BY THE TEST WRITTEN FOR IT (223a0986)

87ab658e asked, of the append-at-an-equal-LT trick: "that rule has to be
believed by EVERY reader of that channel... worth enumerating the readers
before the writer relies on it. If there is only one reader, the trick is free
and this costs you a grep."

IT COST MORE THAN A GREP, AND THE GREP WOULD HAVE PASSED IT. The semantic
readers of a translator channel are exactly two -- provider.Rows (sequential)
and provider.CatchUp (PeekTail) -- plus lookupCached, which dies with
ProjectIncrementally, and the store's own byte accounting, which has no
semantics. By enumeration the trick looked free.

THE TEST SAYS OTHERWISE, on the production log rather than on MemLog
(TestTwoRowsAtOneFigaroLTDivergeBetweenAWarmAndAColdRead):

    append at an equal FigaroLT   ACCEPTED, and BOTH ROWS ARE DURABLE
    PeekTail                      serves the LATER row
    Lookup                        serves the FIRST row
    Read() on the live handle      returns ONE row -- the first
    Read() from a fresh backend    returns TWO

    THE WARM READ AND THE COLD READ OF ONE CHANNEL DISAGREE. The residency
    index is keyed by FigaroLT, which is a FOREIGN key and not unique, so a
    second row at that coordinate is INVISIBLE UNTIL RESTART.

So a corrected row would be invisible to the reader that needs it and would
appear after a restart -- worse than the orphan it repairs, and in exactly the
case where the first row was wrong. The refinement is withdrawn.

THE REPAIR PATH IS THEREFORE: detect by content hash at open (unchanged, still
O(1)), then CLEAR THE TRANSLATOR CHANNEL AND RE-CATCH-UP. Rows are derived
state -- Gluck's own ruling today -- so the cost is one re-encode of one aria,
once, and only after a crash inside the window between the two writes.

AND THE DIVERGENCE IS RAISED SEPARATELY, because it is not mine and it
outlives this design: no channel in the real store carries two rows at one
FigaroLT today (the rows-per-record probe found zero), so it is a latent trap
rather than a live fault. What it costs to close is a question about the
residency index's key, which is section 4's subject already.
