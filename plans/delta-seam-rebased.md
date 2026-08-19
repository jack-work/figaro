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

# THE PROVIDER SURFACE SPEAKS OF A LOG NOW (223a0986, 2026-08-19)

Gluck's rule, on the role form as `vocabulary`: FIGARO CALLS A LOG, NEVER A
CACHE -- the cache is an implementation detail contained entirely within the
canonical log implementation. dfdfae6a pointed out that the provider surface
still said the old word to CALLERS. Renamed, mechanically, in its own commit:

    CacheOpen                -> RowsOpen        (Registration, BuildContext,
                                                 and every provider's field)
    cacheFor                 -> rowsFor
    a.cache / p.cache        -> rows
    CacheNamespace           -> RowsNamespace
    ClearStaleTranslationCache -> ClearStaleRows
    invalidateCache          -> invalidateRows  (copilot)

WHAT DELIBERATELY DID NOT MOVE: AssistantCache, CacheControl, CachePolicy,
CacheCaps, cache_control, MaxCacheBreakpoints and system.cache_markers. Those
name ANTHROPIC'S PROMPT CACHE, which is a real cache belonging to the model
vendor. The rule is about our log, not theirs.

ONE PAIR LEFT, AND IT IS ONE DECISION RATHER THAN HALF A RENAME:
`provider.AssistantCache` and `figaro.commitAssistantCache` name OUR write of
the assistant's native payload into the translator log. dfdfae6a's audit put
AssistantCache on the "stays" list; by the rule's letter it is a row, not a
cache. Left alone pending Gluck, because renaming the function and not the
type it carries would be worse than either.

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
