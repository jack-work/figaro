YOU ARE THE INCOMING BEARER of the figaro state-layer role @980dc16c. I am
f3aa1d0b, handing off at 75% context per Gluck's instruction. He is ACTIVE and
ENGAGED right now and wants momentum: "go go go".

FIRST ACTION: `figaro cast @980dc16c` -- it points the role at you AND studies
it, so its keys arrive every turn. NEVER BIND. Then arm a heartbeat on yourself:
`figla arm --aria <your id> --in 15m --about "<distinct text each time>"` and
re-arm when it fires. USE DISTINCT --about TEXT EACH TIME: figla derives its
systemd unit name from a truncated prefix, so two reminders with the same
leading words collide, the second silently downgrades to a weaker mechanism, and
one fires early carrying the other's message. Verified tonight.

## READ FIRST, IN THIS ORDER

  plans/tree-shaped-log.md on feat/layered-cache -- THE LIVE DESIGN. Its top
    three sections are Gluck's standing orders and they govern everything.
  plans/delta-seam.md -- the closed stage, ~1500 lines, read the last third.
  plans/bytes-to-the-wire-translator.md (feat/delta-seam) and
    plans/bytes-to-the-wire-ir-form.md (feat/layered-cache) -- the call trees.
  plans/campaign-notes/instrument-not-reaching-the-code.md -- 16 instances of
    the one failure this campaign exists to prevent.
  skills/figaro/contributing/maintaining.md -- six maxims, all paid for.

## GLUCK'S THREE STANDING ORDERS, ON THE ROLE BOARD, READ THEM THERE

  single-layer-block   ONE uniform cache layer. A regression is HIS concern,
                       raise it, do not engineer around it. HALT if convergence
                       is violated.
  deletion-default     Existing code does not survive in reduced form. Keeping a
                       partially-obsolete structure needs his EXPLICIT approval;
                       deleting is the default. Bad tests removed, not replaced.
                       Inline explanations purged.
  comms-and-complexity Standard CS terminology, not project slogans. Complexity
                       regressions (O(1)->O(n), O(n)->O(n^2)) need his approval
                       BEFORE landing. Halt when pending approvals would be
                       prejudged by continuing.

## THE DESIGN, AS IT STANDS

ONE CACHE, an implementation detail of the log, ONE PER LOG. Only two layers:
disk and memory, behind a single interface, one policy. It must serve forms,
wals, xwals, derived forms. The TREE-SHAPED cache is the one to keep. GLUCK'S
WORDS: "quit using forest, its a tree."

OPEN AND HIS TO DECIDE: whether figwal moves back INTO figaro. He raised it;
figaro is its only client. I did not answer.

THE BYTES: on-disk cached bytes and wire bytes are identical EXCEPT at known
offsets where cache_control is stamped. Design is: encoder records the insertion
offsets, streaming writer stamps mid-stream, no deserialization. copilot/
responses ALREADY passes bytes through untouched -- existence proof. The other
three decode and re-encode every turn.

BORROWING: eager materialization of the whole lineage path, explicitly pinned
(Cache.Put already has a `pinned` flag), released run by run as the stream
passes. Not lazy -- lazy puts a segment read in the middle of a socket write.

OFFSETS: figaro has NONE. They live in figwal as segment.offsets []int64,
per-segment, file-relative, rebuilt by scanning on open. Segments are NEVER
truncated or compacted; whole sealed segments may be dropped by
disk.Log.TruncateFront. So offsets never shift; the only invalidation is whole-
segment removal. Marker offsets should be computed at encode time and held in
memory, never persisted -- persisting them creates a second durable copy of
derived state, which is the accretion pattern.

## NUMBERS I PROPOSED AND GLUCK HAS NOT YET APPROVED

  SEGMENT SIZE 64 KiB. His rule was "10th percentile and up should have 5
  segments"; on the full population p10 = 139 B exactly (30.9% of arias hold ONE
  record), so p10/5 = 28 B, which is 37,700x below figwal's 1 MiB hard floor.
  UNSATISFIABLE AS STATED. 64 KiB sits between the >=10 KiB and >=100 KiB
  restricted populations; p90 aria gets 7 segments, p99 27, max 114. The real
  argument for it: A MISS READS THE WHOLE SEGMENT, so 64 KiB makes a miss 32x
  cheaper than 2 MiB. It is also the granularity of TruncateFront.
  EVICTION BUDGET 128 MiB encoded, idle-epoch. Top decile of arias totals 300
  MiB encoded on disk, so it cannot hold them all and will evict when several
  large arias are open, while light use never approaches it.

## PENDING HIS APPROVAL

  1. Which of figwal's TWO residency mechanisms to remove (forest/cache.go 407
     lines, runs+LRU per node; segment/cache.go 208 lines, global [][]byte with
     idle eviction). Under deletion-default, RETAINING either needs his word.
  2. The 64 KiB / 128 MiB numbers above.
  3. Whether figwal merges into figaro.
  4. STANDING: complexity report before any consolidation lands.

## IN FLIGHT RIGHT NOW

  fd15d2a0  the chunked-request-body probe: live endpoint + vendor docs +
            internet, reported as THREE SEPARATE ANSWERS. Must verify the
            request actually went out chunked rather than trusting the flag.
            Must not handle the credential directly.
  6ec565b5  just delivered the store census. Available.
  9ed3f561  NEAR END OF CONTEXT. Its harness is rescued at scripts/callpath.
            Expect to replace it.

  UNDONE AND NEXT: THE SPECIMEN RUN. scripts/callpath, translator channel, disk
  to syscall.Pread and back to syscall.Write, full depth, systemd-run to a file,
  assert the artifact not the unit status. It converts the hand-read call trees
  into verified ones. Everything hand-read should be DISCARDED, not merged.

## THE ESTIMATE, CORRECTED ONCE ALREADY

Consolidation removes ~2,000-2,500 lines, concentrated in internal/store and in
figwal, NOT in the providers. My first estimate was ~1,000 and wrong because I
costed each layer as what it would BECOME rather than as deleted -- which is the
accretion's own assumption. Do not repeat it.

## STANDARDS THAT ARE NOT NEGOTIABLE

Gate logs stamp BEGIN and END with HEAD, dirty count, go version, loadavg, and
they must AGREE; a dirty tree carries a DIGEST of the diff. checkgate.sh refuses
anything else. Cross-aria patches are apply-checked against the RECIPIENT'S head
and the rehearsal names the head it was rehearsed at. Canary every test; a
PASSING canary is a finding. Count rather than time where the question is "how
many times". Assert the artifact, never a status field. Corrections go in NEW
paragraphs beside what was read. The claim is written AFTER the verification.

## WHAT I GOT WRONG TONIGHT, SO YOU EXPECT IT

Five of my rulings were corrected by workers, and every correction improved the
record. I upheld a measurement refusal three times without asking what the
measurement was made of. I under-counted the consolidation by a factor of three.
I nearly reported a deletion as landed when it was measured-but-uncommitted. I
created a routing hazard a worker had to name for me. RULE FAST ENOUGH TO BE
WRONG EARLY. If a turn passes with nobody correcting you, be suspicious of the
arrangement rather than pleased with it.

Ring true.
