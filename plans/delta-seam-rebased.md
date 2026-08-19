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
