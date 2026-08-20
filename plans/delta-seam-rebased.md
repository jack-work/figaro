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

## THE REPAIR IS THE STORE'S OWN MECHANISM, VERIFIED RATHER THAN ACCEPTED
## (223a0986, on 87ab658e's pointer)

They said Clear-and-re-catch-up is the designed path, not an improvisation.
Read in internal/store/schema.go rather than taken on trust:

    classDerived            "a cache; clear it, regenerate lazily"
    "translations-v2/"      {version: 2, class: classDerived}
    checkGeneration         a derived channel whose version moved goes into
                            `bust`, and clearDerived empties it
    CheckStoreGeneration    "runs BEFORE anything OPENS the store"

So the store already treats a translator channel as droppable and already owns
the drop. THE TIMING IS THE PART THAT MATTERS FOR THE ORPHAN: the repair must
run BEFORE THE FIRST APPEND, because the first append is what reuses the LT
and turns a detectable orphan into an undetectable one -- and that gate is
positioned exactly there.

ONE PRECISION, SO THE CORROBORATION IS NOT OVERSTATED: CheckStoreGeneration is
per-STORE and fires on a VERSION CHANGE. The orphan check is per-ARIA and per-
OPEN, and needs the fig IR tail beside the row tail. So the gate is the right
PLACE BY ANALOGY and the right precedent for the drop; it is not literally the
hook, and wiring the repair into it would fire it for stores that have no
orphan and never for the one that does.

# THE ENCODER INJECTION HAS A FAN-OUT QUESTION (223a0986, 2026-08-19)

Gluck approved the injection: "i guess irDoor should hold an injected
encoder." One thing has to be decided before it is wired, because the two
answers differ in WORK PER APPEND and not in structure.

    A ROW IS PER PROVIDER. The door writes one record; how many rows does it
    write?

  (a) ONE PER CONFIGURED PROVIDER. Every append encodes for every provider the
      binary knows about, whether or not the aria has ever used it. A user
      with four providers configured and one in use pays 4x the encoding on
      every append, forever, for rows nobody reads.

  (b) ONE PER TRANSLATOR CHANNEL THAT ALREADY EXISTS FOR THAT ARIA. The store
      can see which channels an aria has WITHOUT knowing what a provider is --
      the channel is a name under translations-v2/ -- so the door encodes for
      exactly the providers this aria has actually spoken to. A first send
      through a new provider finds no channel, and the catch-up fills it once,
      which is the path that already exists and is already correct.

(b) is the one that keeps the store ignorant of providers AND keeps the work
proportional to use. Its cost is that the FIRST send on a new provider is
still a catch-up over the whole history -- which is exactly what happens today
and is why the catch-up does not disappear when rows move to write time.

WHAT DOES DISAPPEAR UNDER (b): the steady-state read-path write. After the
first send, every record's row is written by the door at record time, and the
catch-up finds nothing to do -- which is the property the section exists for.

# THE FAN-OUT, DECIDED BY A CENSUS OF THE REAL STORE (223a0986, 2026-08-20)

Gluck approved "one row per translator channel the aria already has", with the
caution that it "might automatically get all of them, and it probably
shouldnt". 87ab658e sharpened it into a requirement: a channel that EXISTS is
not evidence a provider is IN USE, and the set only grows.

MEASURED BEFORE CHOOSING (TestTranslatorChannelCensus, a copy of the real
store, 726 arias). A channel is LIVE if its newest row names a fig IR record
within 50 records of the aria's tail:

    arias with no translator rows at all      402 of 726  (55%)
    channels per aria (of the other 324)      p50=1 p90=1 p99=2 max=3 mean=1.10
    LIVE channels per aria                    p50=1 p90=1 p99=2 max=3 mean=1.03
    fossil channels in the WHOLE store        22

    anthropic          exists on 207 arias, live on 188
    copilot-messages   exists on 139,        live on 136
    copilot-responses  exists on  11,        live on  11

SO EXISTENCE AND RECENCY ARE THE SAME RULE ON THIS STORE: 1.10 against 1.03
channels per aria, and 22 fossils in total. Writing to every existing channel
costs about 6% more encoder work than writing only to live ones, and the worst
aria in the store has three.

    DECIDED: EXISTENCE, because a recency rule buys 6% and adds a threshold --
    a number that would have to be chosen, tuned and explained, and that can be
    wrong in both directions. The census is the evidence, and it is repeatable:
    if fossils ever grow, the same probe says so and the rule changes then.

AND THE OTHER HALF OF THE NUMBER MATTERS MORE: 55% OF ARIAS HAVE NO TRANSLATOR
ROWS AT ALL. For them the door writes nothing at record time and the first send
still catches up -- which is why the catch-up survives this section rather than
being deleted by it.

# BEFORE BUILDING SECTION 2: I DOUBT THE PREMISE OF ROW-FIRST (223a0986)

Raised rather than built past, because the ruling and the reason for it came to
me relayed, and one step of the reason no longer holds.

## THE REASON GIVEN FOR ROW-FIRST

"An orphan record -- a record with no row -- is FATAL under 'a missing row is
an error'; an orphan row is not. Reversing the order converts the fatal
failure into the benign one."

## WHY I THINK THE FIRST HALF IS FALSE

A RECORD WITH NO ROW IS REPAIRABLE, FAITHFULLY, BY THE CATCH-UP THAT ALREADY
EXISTS. The door rewrites the payload BEFORE it lands, so what is on disk IS
the post-repair payload; a later encode of that record produces exactly what
the door would have produced. Nothing is lost by encoding it a moment later --
which is precisely why the catch-up is correct today.

"A missing row is an error" is a rule about the SEND PATH: a send that cannot
read its own conversation must fail rather than assemble a different one. It
is not a claim that a gap can never be filled.

## WHAT EACH ORDER ACTUALLY COSTS

    RECORD FIRST   crash window leaves a record with no row. Repaired by the
                   catch-up, which stays anyway for the first send through a
                   provider (55% of arias have no rows at all). NO HASH
                   NEEDED for safety. The stamp is already an OUTPUT of the
                   append, so nothing more is required -- the hoist is done.

    ROW FIRST      crash window leaves a row at an LT the next append
                   reissues, which is a LIE and not a gap, so it needs the
                   content hash to be detectable. AND IT NEEDS THE STAMP
                   BEFORE THE APPEND, which is the half that is NOT done: the
                   cursors are computed inside xwal under the main channel's
                   lock, and the form channel has its own writer, so a door
                   that reads them first can be overtaken between the read and
                   the append.

## SO THE QUESTION FOR GLUCK, NARROWLY

Row-first buys: no repair path in the common case. It costs: a hash field, and
an xwal surface change to make the stamp an input, with a race to close that
record-first does not have.

Record-first buys: it works with what is already built, today. It costs: the
crash window is repaired by the catch-up rather than by construction.

I RECOMMEND RECORD-FIRST AND I MAY BE WRONG -- the hash is approved either way
and is worth having as an alignment check, and if he wants "every record has a
row BY CONSTRUCTION" as a property rather than as a repair, row-first is the
only order that gives it. That is a design preference I should not settle by
building.

# BEFORE A RESTART, READ THIS (223a0986, 2026-08-20 02:00)

## THE ONE THING THAT BITES: THE SCHEMA BUMP IS ONE-WAY IN PRACTICE

`translations-v2/` moved v2 -> v3 (rows carry the record's content hash).
checkGeneration REFUSES A STORE WRITTEN BY A NEWER BUILD:

    "store channel %q is schema v%d but this figaro understands v%d:
     refusing to open a store written by a newer build (upgrade figaro)"

    SO: IF A BINARY BUILT FROM feat/layered-cache OPENS THE LIVE STORE, THE
    INSTALLED RELEASE CAN NO LONGER OPEN IT until it is upgraded to a build
    that understands v3. The refusal is by version and does not care that the
    channel is derived.

That is not a corruption and nothing canonical moves -- the fix is to run a
newer figaro -- but it is a door that only swings one way, and it should be
walked through deliberately rather than by accident.

    RUN THIS BRANCH AGAINST A COPY UNTIL IT IS MEANT TO BE INSTALLED.
    Every probe in this campaign already does (FIGARO_REAL_STORE /
    FIGARO_MIGRATED_STORE take a COPY, and the tests say so).

## WHAT ELSE CHANGES ON A RESTART OF A BUILD FROM THIS BRANCH

  1. EVERY ARIA'S TRANSLATOR ROWS ARE DROPPED ONCE and re-derived on that
     aria's next send. 324 of 726 arias have rows; the rest have none. The
     cost is one catch-up per aria, at its next send, and it is the mechanism
     the derived class exists for.
  2. NEW ARIAS TAKE THE RAW ANTHROPIC PROVIDER. ~/.config/figaro/outfits/
     house.toml now sets use_official_sdk = false. EXISTING arias carry the
     old value on their own boards and keep the SDK path until patched.
  3. COPILOT-MESSAGES RUNS ON THE RAW PROVIDER regardless of that key, and its
     fingerprint changed with it -- subsumed by (1), since the rows drop
     anyway.

## WHAT IS IN FLIGHT: NOTHING

No background jobs, no benchmark runs, no reminders armed for this role, no
temp worktrees or store copies of mine (the census copy and the migration copy
are deleted; /var/tmp/fig-base-223a is removed). The tree is clean at
50a7b4e3, the full suite is green, and NOTHING IS PUSHED -- the branch is
local, so a restart that does not build from it changes nothing at all.

# FOLLOW-UP, GLUCK 2026-08-20: THE SYSTEM PROMPT IS NOT IN THE HISTORY

The board has TWO temporalities today and only one of them is recorded:

    board-as-current   the SYSTEM PROMPT. turn.go samples a.form.Snapshot()
                       once per send and systemBlocks() builds the block from
                       it. Nothing is stored, so editing system.credo changes
                       the system prompt of every past conversation the next
                       time each is sent.
    board-as-of-LT     the in-message form transitions. Each fig IR entry
                       carries FormChannelVersion; the Deriver folds
                       PatchesBetween at that stamp, so a re-translation
                       renders the reminder the model actually saw.

GLUCK: "note the follow up work to make the credo and starting message a
proper entry in the fig ir and translator ir history, but for now, we can
leave as is."

WHAT IT WOULD BUY: a replay reproduces the exact request, and the first thing
a figaro was told becomes part of its history rather than a function of
today's config. WHAT IT COSTS: a credo edit stops reaching arias that already
exist, so an outfit change becomes a migration rather than a fact.
NOT STARTED.
