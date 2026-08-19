# Q3'S THIRD TENANT: THE COMPOSED UI IR ON THE CANONICAL TREE

dfdfae6a, 2026-08-19, holding @980dc16c. Written BEFORE any code, because the
handoff named one decision as the thing to make first and in writing: the
composed layer's API is POSITIONAL where the two tenants already re-seated were
LT-KEYED.

The reading and the parity number are dec6ef8a's, in plans/tree-shaped-log.md
("the whole board", and the runChunk table): `internal/livelog/aria.TurnCache`
is tree.Cache's design written a third time -- hollow entries, an index that
survives eviction, a source that rematerializes, a budget with the same three
numbers, per-entry pinning that stays counted, and both files quote the same
post-mortem in the same words. At matched run granularity it faults within 1.3%
of tree. Nothing below re-argues that; this decides HOW it lands.

## THE DECISION: THE COORDINATE IS THE TURN'S OPENING LT, AND THE TENANT KEEPS
## AN ORDERED KEY LIST

Two candidates were considered. Both were checked against the code rather than
against the shape of the API.

    TURN ID AS THE COORDINATE. Turn ids are dense (turns.StampIDs, a.turnID++)
    and they are what a reader passes -- Anchor.Turn is a turn id. tree would
    get its dense fast path for free.

    THE TURN'S OPENING LT AS THE COORDINATE. Sparse. Not what a reader passes.

LT WINS ON THE TWO SEAMS THAT MATTER, and both are seams the turn-id key would
have to invent a translation for:

  1. THE LINEAGE. `tree.Ref.Base` is a fork base, and every fork base in this
     system is an LT (store.Lineage, and treeLog's refs). Under LT coordinates
     the composed cache reuses THE SAME lineage function the decoded IR tenant
     already has, and a fork's composed prefix is its ancestor's runs by
     construction. Under turn-id coordinates every base needs an LT -> turn-id
     translation, which is a read of the record at the base to learn its
     TurnID -- a lookup that can miss, in the one place the plan says a miss
     becomes a lie about whose history you are serving.

  2. THE SOURCE. `TurnSource(fromLT, toLT)` is already an LT bracket on both
     implementations (Agent.turnSource, AriaReader.turnSource), and both spend
     it directly on `log.ReadPage(fromLT, toLT+1, ...)`. Under LT coordinates
     the source signature SURVIVES UNCHANGED and `Source(Coord)` is a rename.
     Under turn-id coordinates the source must translate a turn range into an
     LT bracket on every miss.

And the cost of choosing LT is the one thing that does not matter here: the key
space is sparse, so `tree.At`'s dense fast path does not apply. THE COMPOSED
LAYER HAS NO POINT READ. Every read on this tenant is `Slice(lo, hi)` from
`Server.page`, i.e. a range; the point-read cost that made Q1 a question for the
segment tenant does not exist on this one.

    THE COMPOSED CACHE AND THE DECODED IR CACHE END UP ADDRESSED IN THE SAME
    COORDINATE SPACE, BY THE SAME LINEAGE, OVER THE SAME SUBSTRATE. That is
    what "one uniform layer" buys that a third structure cannot: a composed
    run and the IR run it was composed from name the same bracket.

`compose.Turns` sets `LTs: []uint64{first, last}` per turn, first = the LT of
the message that OPENS the turn, last = the LT of its final message. So turn
brackets ascend and do not overlap (they have gaps: boot/state tics before the
first prompt belong to no turn). That is a legal sparse key space for tree: a
run is a bracket, not a row -- the same property the translation channel already
relies on, where one coordinate holds several units.

### THE POSITIONAL HALF, WHICH DOES NOT GO AWAY AND SHOULD NOT

`Len`, `IndexOf`, `ChunkFor` and `Slice(lo,hi)` are index-shaped, and
`ChunkFor` plans a page FROM THE PER-TURN SIZES -- "exact, not guessed". tree
cannot serve that: hollowing keeps `{coord, bytes}` PER RUN and drops the
units, so per-turn keys, brackets and sizes are gone the moment a run is
hollowed.

    SO THE TENANT KEEPS AN ORDERED KEY LIST: id, LT bracket, At, Sealed, bytes.
    ~48 B per turn; a 400-turn aria's whole index is under 20 KiB.

THIS IS NOT THE SECOND RESIDENCY POLICY THE STANDING BLOCK FORBIDS, and the
distinction is worth stating plainly because it is exactly the kind of thing
that gets waved through: the key list has NO budget, NO eviction order, NO LRU,
NO pinning and NO lock. It is the tenant's KEY SPACE MAP -- the same smaller
job `registry` (node path -> *Segment) kept when the segment tenant's duplicate
residency structure was deleted. Residency, eviction and rematerialization all
become tree's, and there is exactly one of each.

## WHAT IS DELETED

    UIBudget                        the whole accountant: limit/total/lru,
                                    victimsLocked, pending, settle, evictions,
                                    the process-global mutex
    turnMeta.resident/counted/elem  residency bookkeeping
    hollow/account/touch/releaseAll the eviction path
    pinned + unbracketed            see below: pins stop existing
    seed_turns.go                   the composed prefix DONATION and its seam
                                    (donatedSeam, spliceDonated) -- the fourth
                                    hand-written seeding path in this system,
                                    replaced by lineage

`aria.NewUIBudget` becomes a `tree.Budget` with the same config number
(`ui_window_mb`), swept on the daemon's standing beat beside the other two
(`Angelus.sweepCacheBudgets`), and `doctor mem`'s UIWindow* fields read
`Budget.Stats()` as they already do.

### AND THE PINS STOP EXISTING, WHICH IS THE SECOND SIMPLIFICATION

Today the tail is pinned because the Server MUTATES IT IN PLACE (Close folds
the open suffix in, Seal stamps the bracket, OpenInquiry writes the question),
and a tree run is immutable once published. `TailMutated` re-tallies it and
re-derives the pin; `victimsLocked` carries a tail-index guard; `unbracketed`
exists for it.

    THE TAIL IS NOT PUT IN THE CACHE AT ALL. It is a staging slot on the
    tenant -- the newest turn, whatever its state -- and it enters the tree
    when a NEWER turn displaces it, by which time it is sealed and its LT
    bracket is known, so it is recomposable like everything else.

An unbracketed SEALED turn (a legacy record at LT 0) is the one remaining case
that cannot be recomposed; it is `Put` pinned, which is tree's own word for it,
and it is a property of that turn rather than a latch on the cache. The v1
defect the comment records -- one such turn disabling eviction for every aria
after it -- cannot recur in a shape where the pin is a run flag.

## THE HAZARD, AND IT IS THE ONE THE PLAN DEMANDS BE TESTED BELOW A FORK BASE

A fork base is an LT and it can fall INSIDE a turn. The child's log then holds
that turn's opening messages (inherited) plus its own continuation: SAME TURN
ID, DIFFERENT CONTENT. If the straddling turn's key (its opening LT) is below
the base, a lineage read serves the PARENT'S composed turn for it -- a wrong
lineage link, which is the failure mode "a wrong lineage link serves another
aria's history as your own", invisible to any single-lineage fixture.

    THE CURE, BY CONSTRUCTION: THE COMPOSED LINEAGE'S FORK BASE IS THE IR BASE
    SNAPPED DOWN TO A TURN BOUNDARY -- to the opening LT of the turn that
    contains it, minus one. The straddling turn then belongs to the child's own
    node and is composed from the child's own records.

The snap is computable from the tenant's own key list (the child composes its
own history today, so it knows every bracket) and it costs one binary search
per lineage build, not per read.

FIRST TEST WRITTEN, before the code compiles: a parent, a child forked
MID-TURN, and an assertion on the straddling turn's content against a fresh
unseeded composition of the child's own log -- plus the canary that removes the
snap and must go red. Everything at or above the base agrees whatever you do,
which is why the assertion is below it.

### THE STRADDLE IS NOT HYPOTHETICAL -- CHECKED AFTER THE PARAGRAPH ABOVE WAS
### WRITTEN, WHICH IS THE WRONG ORDER AND IS RECORDED AS SUCH

`store.ForkAt(ariaID, atMainLT)` is documented as "an interior fork" and takes
an arbitrary main-LT; `ForkWith` (the edit path) takes one too. `Lineage`
renders `Ref{Node, Base: t.BranchedLT}` -- a raw LT, with no turn boundary
anywhere in it. So a fork base falling inside a turn is a shape the store
offers on purpose, not an edge case I invented. The snap is required.

## THE SOURCE DISPATCH, WHICH THE SHARED CACHE FORCES AND WHICH DELETES A
## SECOND DUPLICATE

One cache with a node per aria is what makes fork sharing structural (the
decoded IR tenant's own comment: "a cache per log would give each aria its own
nodes map and share nothing"). But `tree.Source` is per-CACHE and receives
`Coord.Node`, while `TurnSource` today is per-SERVER and bound by whoever owns
the Server -- `Agent.turnSource` for a live aria, `AriaReader.turnSource` for a
dormant one, TWO NEARLY IDENTICAL FUNCTIONS (read the LT bracket, compose,
attach form deltas with a seed from the record before the bracket).

A child reading its inherited prefix asks for the ANCESTOR'S node, and the
ancestor is frequently not open in this process. Under a per-Server source that
read cannot be served at all and the page would show a gap -- which is how the
decoded tenant's `openNode` came to exist: it resolves an ancestor node to its
SUBSTRATE rather than to a live handle.

    THE COMPOSED SOURCE IS THEREFORE ONE FUNCTION PER PROCESS, KEYED BY NODE
    AND BACKED BY THE BACKEND: open node N's log, read the bracket, compose,
    attach deltas. It serves a live aria and a dormant one identically, because
    below it sits the same decoded-IR tree, warm or not.

`Agent.turnSource` and `AriaReader.turnSource` both go. That is a fifth
duplicate removed by this re-seat, and it was not visible from the API shape --
it fell out of asking who answers a miss on a node nobody has opened.

## COMPLEXITY, STATED AS THE RULE REQUIRES

No operation changes asymptotic class:

    Server.page / Slice      range read              unchanged (Range vs a
                                                     slice of the same span)
    IndexOf                  binary search over the key list        unchanged
    ChunkFor                 walk from an anchor, budget-bounded    unchanged
    recompose                one source call per contiguous gap     unchanged
    boot (Restore)           composes the whole history, as today   unchanged

The one thing that gets CHEAPER is a fork's open: today `seed_turns.go` splices
a donated prefix and composes the suffix; under lineage the prefix is the
ancestor's runs and nothing is composed for it. Making boot LAZY (index without
composing) is a further change and is NOT part of this one -- the index carries
per-turn byte sizes that only a materialization knows, and ChunkFor would have
to guess where today it is exact. Named here so it is not done by accident.

## WHAT I AM NOT DECIDING, AND WHY IT IS NOT BLOCKING

  - ONE POOL VS THREE BUDGETS. This lands as a third `tree.Budget` carrying
    `ui_window_mb`, which is what tree's own comment already anticipates ("one
    per concern... one pool tomorrow is a config choice, not a rewrite"). It
    does not prejudge the pool question either way.
  - THE RUN LENGTH KNOB. dec6ef8a measured that fault rate follows granularity
    and that `runChunk` wants to be a BYTE TARGET, and `cutByBytes`/`runTarget`
    already cut by bytes on the current head. Nothing here re-opens it.
  - Q1 (the dense fast path) and Q2 (the eviction index) are untouched by this
    tenant: it has no point read, and it adds runs to the same budget whose
    sweep cost is Q2's subject -- which is an argument for Q2, not against
    this.

---

# WHAT LANDED, AND THE TWO THINGS THE INSTRUMENTS SAID (dfdfae6a, 2026-08-19)

Step A: the composed layer is on tree.Cache. `UIBudget` is gone -- the
accountant, the `container/list` LRU, `victimsLocked`, the pending-victim
queue, `settle`, `account`/`touch`/`hollow`/`releaseAll` and the
process-global mutex with them. What replaced it is `ComposedCache` (one
`tree.Cache[Turn]`, a node per aria, registered per-node source closures) and
a `turnKey` list per tenant. 485 lines of turncache.go became 500 of which
~180 are the key list and the Server surface; the residency policy is now
somebody else's, counted once, swept on the daemon's standing beat beside the
decoded budgets.

`config.ui_window_mb` still tunes it, `doctor mem` still reports it -- through
`tree.Budget.Stats()` now, which is the same three numbers every other layer
reports.

## THE PINS WENT AWAY AS PREDICTED, EXCEPT FOR THE ONE THAT CANNOT

The staging tail is not in the cache at all, so `TailMutated` is a re-tally of
one index entry and nothing is pinned for being mutable. A SEALED turn with no
LT bracket -- a round that failed before it wrote a record -- still cannot be
recomposed and is held pinned and COUNTED, keyed in a reserved region above
2^63 where no logical time reaches. It pins itself and nothing else, which is
the S1 fix restated in tree's own vocabulary rather than in a second one.

## A CANARY THAT PASSED, AND WHY

The first canary on the bracket snap -- replace `c.bracket(from,to)` with
`from+1, to` -- LEFT EVERY TEST GREEN. The explanation was not that the snap
is unnecessary:

    A RUN HELD EXACTLY ONE TURN, AND ITS COORDINATE SPANNED THAT TURN'S WHOLE
    RECORD BRACKET, so the snap was the identity function on every fixture I
    had. The canary was measuring a case where both branches agree.

The cure was not a better canary, it was a coordinate convention: a run's span
is a KEY RANGE (from the previous turn's key to this turn's key), not a record
range. Nothing addresses a turn by its interior records, and two Put sites had
been spelling the span two different ways -- `flushTail` by record bracket,
`seed` by key. With that settled the canary bites: both content tests go red
and green on restore.

    THE SECOND FIXTURE IS THE ONE THAT COULD SEE IT ANYWAY: drop the node's
    runs and let the whole history fault back through the cache's OWN gap
    chunking, which lands wherever runChunk falls -- squarely inside turns.
    That is the only path where the source is asked for a bracket nobody
    aligned, and it is exactly the path a fork's inherited prefix will take
    in step B.

## AND ONE THING THIS RE-SEAT DID NOT FIX, NAMED SO IT IS NOT MISTAKEN FOR DONE

A live agent and a reader can hold servers for the SAME aria at once. They
still hold TWO composed copies of its sealed turns -- now on two nodes
(`id` and `read:id`) against ONE budget and one eviction order, where before
they were two structures with two accountants. One node for both needs a
tenant object shared by two Servers with one key list, which is a bigger
change than this one and is not started.

## STEP B, NOT STARTED

The fork seam: lineage refs on the composed node (bases SNAPPED DOWN to a
turn boundary), one backend-based composer per process replacing
`Agent.turnSource` and `AriaReader.turnSource`, and the deletion of
`seed_turns.go` -- the composed prefix donation, `donatedSeam` and
`spliceDonated`. The hazard test named above (a fork MID-TURN, asserted below
the base) belongs to that step.

---

# STEP B: THE FORK SEAM IS STRUCTURAL, AND THE DONATION IS DELETED (dfdfae6a)

## WHAT WENT

    internal/figaro/seed_turns.go       the composed prefix DONATION, its seam
                                        detector and its splice (-129 lines,
                                        -237 of test)
    Agent.TurnDonor / turnDonor         the config knob and the field
    Angelus.TurnDonor                   the live-ancestor walk
    Agent.TurnsBelow / turnBelow        the donor half
    Agent.turnSource                    one of two near-identical composers
    AriaReader.turnSource               the other
    aria.TurnSource                     the type they shared

What replaced all seven is `Angelus.composeTurns(node, fromLT, toLT)`: ONE
function, keyed by node, backed by the store. It answers for a live aria, a
dormant one, and an ancestor nobody has opened, which is what a fork reading
its inherited prefix needs and what a per-server source structurally could not
do -- the source was reachable only through the Server that owned it.

## THE PREFIX IS THE ANCESTOR'S RUNS

`TurnCache.put` skips any turn whose key is below its node's fork base: those
turns live in the ancestor's node and are read through `tree.Range`'s lineage
walk. The seam probe is not preserved -- it is UNNECESSARY BY CONSTRUCTION,
which is what the plan asked for: there is no copy to verify, because there is
no copy.

## AND THE BASE IS SNAPPED, WHICH THE CANARY PROVED IS LOAD-BEARING

`store.ForkAt` and `ForkWith` take an interior LT, so a fork base falls inside
a turn whenever the fork cuts mid-turn -- which is the ordinary case for an
edit. The child's version of that turn is its opener (inherited) plus its OWN
continuation: same turn id, different content.

    TestAForkBelowATurnBoundaryServesItsOwnContent, canary applied:
        got  "PARENT ANSWERS TWO"
        want "CHILD ANSWERS TWO"

That is a wrong lineage link -- another aria's history served as your own --
caught by an instrument rather than by a reading. The cure is one line: a base
that falls at or inside a turn's bracket is lowered to that turn's KEY, so the
child owns it outright. Base is the FIRST coordinate the child owns, which is
the convention store's own forkbase test pins, and my first draft had it off
by one in the other direction.

### THE FIXTURE'S FIRST TWO VERSIONS TESTED NOTHING, AND BOTH LOOKED FINE

  1. THE STRADDLING TURN WAS THE TAIL. The tail is the staging slot: it is
     never in the cache and never read through the lineage, so the canary
     passed. One more turn in the child displaces it and the canary bites.
  2. THE BRACKET REPAIR HAD NO WITNESS AT ALL. Removing
     `tailOfLastTurn` -- the repair that completes a turn the bracket cut at
     its END -- left the ENTIRE SUITE GREEN, because every fixture composer
     returns whole turns by construction (they model the contract rather than
     implement it). The witness had to be written against the real composer:
     ask it for a bracket that ends on a turn's second record and check the
     turn comes back with all three of its nodes.

    A CANARY IS ONLY EVIDENCE ABOUT THE FIXTURE IT RAN ON. Two of the three I
    ran today passed on the first attempt, and neither meant what it looked
    like it meant.

### AND THE STAMP THE REPAIR MUST NOT TRUST

The first repair walked forward while `Payload.TurnID` stayed equal. TURN IDS
ARE STAMPED AT COMPOSITION, NOT AT WRITE, for records written before turn ids
existed -- the fixture's own records carry zero -- so that rule stops at the
first unstamped record and truncates the turn silently. The boundary is
`turns.Opens`, which is derived from the record itself.

## KNOWN, NOT FIXED, AND NAMED SO IT IS NOT DISCOVERED AS A SURPRISE

  - A FORK STILL COMPOSES ITS WHOLE PREFIX AT BOOT and then declines to cache
    the part that is not its own. The donation avoided that work when a live
    ancestor happened to be registered; the tree avoids the MEMORY always and
    the WORK never. The fix is to compose only above the base and take the
    prefix's key list from the ancestor's runs -- it needs per-turn sizes,
    which only a materialization knows today, so it belongs with the lazy
    index rather than here.
  - THE READER AND AN AGENT SHARE ONE NODE. Reads for a live aria route to the
    agent (handlers.liveAgent), so they overlap only while an aria is waking;
    a reader Restore in that window drops the node and the agent refaults from
    the store. Correct, occasionally wasteful, self-healing -- and the payoff
    is that a dormant aria's composed turns survive its waking.

---

# STEP C: `fig show` GOES THROUGH THE API (Gluck, 2026-08-19)

His words: "fig show should probably go through the cache", then "the cli
should never read any of the aria state directly, all should be through the
api", and "fig show should call a jsonrpc api that composes the turn server
side and renders on the client. No client turn construction from raw ir should
be on the client" -- with the concession that "if the cli client keeps a
duplicate of the ir for rendering that might be fine since it crosses a
runtime".

## WHAT IT WAS

`show` read RAW IR (`aria.read`, a backward walk in 1000-entry pages) and
called `compose.Turns` IN THE CLI. It then rendered `[]aria.Turn` and, under
`-j`, printed an `aria.Page` -- the same shape the daemon serves. So the
client was doing the daemon's composition to reach the daemon's own output
type: A THIRD COMPOSITION OF THE SAME DATA, after the agent's and the
reader's, and the one that could disagree with both.

`aria.page` -- the RPC that answers exactly this question -- HAD NO CALLER IN
THE REPO. That is the tell: the API existed and `show` never adopted it.

## WHAT IT IS

`show` pages composed turns from the daemon and renders them. The client's
only assembly left is rejoining a turn the WIRE BUDGET cut in half
(`TurnPart.From`), which is a seam the transport introduced, not a projection.
`--verbose/--literal` still read raw IR, because those views render RECORDS
and construct no turns.

Deleted from the CLI: `gatherShowWindow`'s turn arithmetic, `composeTurns`,
`windowSatisfies`, `derivedIDs`, `trimPartialHead`.

## THE PARITY CHECK, WHICH IS THE ONLY REASON TO BELIEVE ANY OF THIS

Old binary and new binary, the SAME daemon, a reflinked copy of the real
store, 12 arias x 4 selectors (`-a`, `-n 5`, `--from 2 --to 4`, `--before 3`),
`-j` output compared with `cmp`:

    33 of 48 BYTE-IDENTICAL, and the 15 that differ are two classes, both
    understood and neither accidental.

### AND THE THREE DEFECTS THE REAL STORE FOUND THAT NO FIXTURE DID

  1. A ZERO ANCHOR MEANS THE HEAD, NOT THE TAIL. `aria.page` chose its
     direction by `Before > 0`, so "the last 40 turns" with no anchor asked
     for a FORWARD page from the head and got the FIRST 40 -- a confident
     wrong answer with no error. ReadRequest/AriaPageRequest grew `Backward`.
  2. A BACKWARD ANCHOR IS (TURN, NODE), NOT A TURN. Paging back from a turn
     id alone asks for everything before that turn's NODE 0, silently dropping
     the rest of a turn the budget had cut. Measured: a 143-node turn came
     back with 9.
  3. A TURN SPLIT ACROSS TWO PAGES MUST BE WELDED. Without it the same turn
     appears twice, and a consumer keyed by turn id keeps whichever fragment
     it saw last.

    ALL THREE PRODUCED PLAUSIBLE, NON-EMPTY, WRONG OUTPUT. The unit suite was
    green through every one of them, and the instrument that caught them was
    a byte comparison against the code being replaced.

### AN UNANSWERED QUESTION WAS INVISIBLE TO EVERY PAGINATED READ

Two arias of twelve came back EMPTY through the API and showed their content
through the raw one. Their whole history is one prompt with no answer, and
`Paginate` walks (turn, node) positions: a turn with an inquiry and no nodes
has NO positions, so it cannot be located, cannot be stepped to, and an aria
made of one returns an empty page. This was never a `show` defect -- THE PAGER
HAS THE SAME BLIND SPOT, and nobody had looked through that door.

Fixed in the paginator, not in `show`: a turn with an inquiry occupies one
position whether or not it has nodes. TestAnInquiryWithNoAnswerIsStillAPage.

### THE ONE DIFFERENCE THAT IS A FIX

`--before N` reported `more.after: false` unconditionally. It is now true when
turns exist after the anchor, which they do.

### AND THE ONE RAISED TO GLUCK, NOW ANSWERED BY THE FORK MODEL

For a fork's INHERITED records, the bound-board form deltas are now keyed by
the ANCESTOR node that owns them rather than by the aria being read:

    -  "86d12409.cwd": {"form": "86d12409"}   the reader
    +  "01efd291.cwd": {"form": "01efd291"}   the owner

ANSWERED, GLUCK 2026-08-19: "for bound forms, the fork should create its own
forked form at the fork point... Once a child is forked, its bound form is
unique. All latter patches to the ancestors form should happen on the
ancestors branch. For unbound forms, the identity is independent from figaro,
so whatever form id figaro sees is what should be recorded."

THAT IS WHAT THE STORE ALREADY DOES, and `CreateForm` says so in its own
words: "a bound form's fork is the aria's fork and it goes through ForkWith".
A bound form has no separate identity -- it IS the aria's chanForm channel on
the aria's trunk node -- so forking the aria forks the form at the fork point.

SO THE OWNER-KEYED LABEL IS NOT A PREFERENCE, IT IS THE MODEL: a record below
the fork base changed the ANCESTOR'S board, because the child's bound form did
not exist yet. The old label asserted that a form changed before it existed.

AND IT WAS NOT ONLY A LABEL. The old path called FormPatchesBetween(CHILD, ...)
for an inherited record -- asking the child's form for a version range stamped
on the ancestor's. The new path asks the aria that owns those versions, so the
change corrected a READ as well as a name.

Unbound forms are untouched and already match: foldStudied keys by the form id
carried in the record's own StudyVersions.
