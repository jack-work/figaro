# Fork admission: invariants checked in O(height), forks never wait

Status: proposal, revision 2 of [Part 2 of the laws proposal](README.md#part-2-the-fan-out-quota),
2026-11-18. Written by the fork-admission lane (figaro `fb9d24b5`, a fork of
the Figaro Engineer) against figaro v0.74.1
([`adccceea1`](https://keel.kelliher.info/_keel/jack-figaro/diff/adccceea199a884cf3cbcbb5bb58a3da9fe87e81)).
Prototype on branch `lane/fork-admission`
([`257fa9940`](https://keel.kelliher.info/_keel/jack-figaro/diff/257fa9940d0b4f657f1bdeb816a35f9dcf651da4)),
measurement harness on `scratch/fork-admission`
([`67da356c1`](https://keel.kelliher.info/_keel/jack-figaro/diff/67da356c13981695e948cc007e7c45eebf1f2365)).

Gluck asked, on Part 2:

> In addition to the quota, I want still to enforce max children. [...]
> invariants should be checkable in constant height time. [...] I don't want
> to block forking either. [...] We could preemptively fork, but block LLM
> calls, and then tombstone before the call goes off if the invariant check
> fails.

Short answers:

- **Both keys stay.** `system.fanout.max_children` caps direct live
  children; `system.fanout.quota` caps the subtree. Part 2's "Migration from
  `max_children`" is withdrawn.
- **One admission step, O(height), in memory.** A ledger in the daemon keeps
  two counters per node (live children, used budget), rebuilt at start in
  0.48 ms and kept by every birth, retirement, revival, move and limit
  change. A fork's check walks from its parent to the nearest capped
  ancestor: 0.4 to 1.7 microseconds at depth 1 to 32.
- **No hierarchy lock on the fork.** The check reserves its charge under the
  ledger's own mutex, held for that walk and never across I/O, then the
  store birth runs with no topology lock, then the reservation commits or is
  given back.
- **The optimistic fork is not needed** on one node: admission is five
  orders of magnitude cheaper than the birth it would hide behind. It earns
  its cost only when admission needs a network round trip (a capped
  ancestor on another troupe node).
- **The thing that makes forks slow and serial today is the store birth**,
  not admission and barely the hierarchy lock. Numbers below.

## Measured

Store snapshot of 2026-11-18 (`figaro doctor snapshot --arias 0 --max-mb 0`,
2,543 nodes, 2,082 figaros, 2.8 GB), opened by an in-process daemon
(`Angelus` + `Handlers`, real `XwalBackend`), never the live store. Run
through `heavy.sh` in the bench class, nothing else running. The harness is
`internal/angelus/fork_admission_snapshot_test.go` on `scratch/fork-admission`.
Target parent: the widest conversation parent, `17664eaf` (57 drawn
children at the start, 91 live by the capped runs).

| what | p50 | p90 |
|---|---:|---:|
| fork RPC, head fork, uncapped parent | 63.9 ms | 65.5 ms |
| fork RPC, head fork, capped parent | 65.7 ms | 67.9 ms |
| `ForkWith` alone (the store birth) | 57.5 ms | 61.1 ms |
| `admitFork`, uncapped parent | 0.21 us | 0.31 us |
| `admitFork`, capped parent (today's scan) | 181 us | 190 us |
| `topology().Children(parent)` | 168 us | 198 us |
| `chainOf(parent)` | 20 us | 24 us |
| hierarchy `WithLock`, uncontended | 0.03 us | 0.04 us |

Concurrency, wall clock:

| forks at once | through the fork RPC | store births alone, no hierarchy lock |
|---:|---:|---:|
| 1 | 67 ms | 60 ms |
| 4, one parent | 262 ms | |
| 8, one parent | 527 ms | 439 ms |
| 8, two unrelated parents | 504 ms | |

The prototype ledger (`internal/admission`, `go test -bench`, 16 threads):

| what | per op |
|---|---:|
| reserve and abort, depth 1 | 0.40 us |
| reserve and abort, depth 8 | 0.68 us |
| reserve and abort, depth 32 | 1.74 us |
| reserve and abort, 16 goroutines under one cap | 0.53 us |
| rebuild over 2,543 nodes | 0.48 ms |

What the numbers say:

1. **Today's capped admission is O(nodes), not O(children).**
   `liveChildrenOf` calls `TopologyTree.Children`
   ([`internal/store/topology_form.go:202`](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/store/topology_form.go)),
   which copies every override edge and walks every node in the store to
   find one parent's children, then reads `_meta` per child. 181 us is 0.3%
   of a fork, so it hurts nobody yet, but it grows with the store, and Part 2's
   quota would need it at every level of the path.
2. **Forks are already serial, and admission is not why.** Since
   `d11779ff1` the fork holds the hierarchy mutex
   ([`internal/angelus/topology_handlers.go:360`](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/angelus/topology_handlers.go))
   across `ForkWith`, a 57 ms store birth with an fsync. But
   `XwalStore.forkWithKind` holds the store mutex across the same birth
   ([`internal/store/xwal_store.go:451`](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/store/xwal_store.go)),
   so eight births with no hierarchy lock still take 439 ms, 55 ms each.
   Dropping the hierarchy lock from the birth buys 17% under contention
   (527 to 439 ms) and nothing for a lone fork. Forks under unrelated
   parents serialize just the same (504 ms).
3. **Locks the fork path takes today**, in order: the parent's form read
   (no lock beyond the form's), the hierarchy mutex `Hierarchy.mu` across
   `admitFork` and `ForkWith`, inside it `XwalStore.mu` across the trunk
   fork, `writeBirth` and its `SyncCoherent` fsync, then per-study libretto
   `Retain` (each an fsync batch), and for an interior cut under another
   trunk a `place` that takes `Hierarchy.mu` again and writes `@topology`
   with its own fsync. The 2026-11-16 latency lane
   ([fork-paint-latency](https://keel.kelliher.info/_keel/jack-figaro/blob/c37b86793fd907eadc28a13a2456ee0ab68a3f92/docs/proposals/fork-paint-latency/README.md?view=preview))
   has the per-phase breakdown of the birth.

So the answer to "forks must be quick" is in two places: admission must
never hold a lock across I/O (this proposal), and the store birth must stop
being a 55 ms global critical section (the latency lane's P3, and a
per-trunk lock in `XwalStore`, out of scope here).

## The admission step

One place in the daemon, the **ledger** (`internal/admission`), owns every
fork invariant. It is the only code that answers "may this node gain this
charge".

### What it keeps

Per node, in memory:

| field | meaning |
|---|---|
| `parent` | the drawn parent |
| `live` | not retired, not reaped |
| `limits` | `max_children` and `quota`, the effective values (the node's own, lowered by every role it holds) |
| `kids` | live direct children, reservations included |
| `used` | the sum of its children's charges, reservations included |

The charge of a node `c` to its parent:

- live and capped: `1 + quota(c)`
- live and uncapped: `1 + used(c)`
- retired or reaped: `used(c)`

**A correction to Part 2.** Part 2 charged an uncapped live child "1 plus
the live figaros under it". That lets a grant escape: the King at quota 3,
an uncapped lead under it, and under the lead a lane granted quota 10. The
lead holds one live figaro, so it charges 2, yet its subtree may grow to 12.
Charging `1 + used(c)` counts the lane's grant, and the King's quota bounds
the subtree by induction again.

### The walk

A change of `d` to a node's charge walks up from its parent: each node on
the way gains `d` in `used`; a node that is capped refuses if `used + d`
would pass its quota; the walk stops at the first live capped node, because
that node's own charge is fixed at `1 + quota`. So the cost is the distance
to the nearest capped ancestor, at most the height. `max_children` is one
more comparison at the parent.

Every operation is that walk:

| event | change | checked |
|---|---|---|
| fork | reserve a live child with charge `1 + grant` | `max_children` at the parent, quota up the path |
| fork fails after reserving (birth error, sandbox grant, studies) | abort: the same walk with `-d` | no |
| fork lands | commit: the reservation takes the child's id; counters unchanged | no |
| `figaro new` by a minter (Part 2, labeled mint) | as a fork, charged to the minter | as a fork |
| retire, reap | `charge` drops to `used(c)`; parent's `kids` drops | no |
| restore, revive | the reverse | yes, refused whole |
| reparent of `s` from A to B | detach `charge(s)` from A's path, then attach to B's | yes at B, refused whole, A untouched |
| kill, sweep, purge | remove the node (children lifted first are moves) | no |
| quota or `max_children` written, or a role taken or dropped | `charge` changes by new minus old | yes; a quota below `used` is refused |
| succession swap | the moves and limit swaps of a demote, in that order | as a reparent |

The prototype implements every row but the wiring:
[`internal/admission/ledger.go`](https://keel.kelliher.info/_keel/jack-figaro/blob/257fa9940d0b4f657f1bdeb816a35f9dcf651da4/internal/admission/ledger.go).

### Where it hooks in

Three choke points already exist, so the ledger needs no new event bus:

- **Births**: the fork handler and the mint path. The fork reserves before
  `ForkWith` and commits or aborts after.
- **Liveness**: `XwalBackend.mirrorLifetime`
  ([`internal/store/ttl.go:303`](https://keel.kelliher.info/_keel/jack-figaro/blob/adccceea199a884cf3cbcbb5bb58a3da9fe87e81/internal/store/ttl.go))
  is the one place retirement and reaping change, and logs both. It gains a
  callback.
- **Moves and deletes**: `Hierarchy.commit` and `Hierarchy.Under`. The
  guard's `fanout` check (today a recount per arriving parent) becomes a
  ledger `Move` taken just before the commit, given back if the commit
  fails, through a new optional guard method beside `AfterCommit`.
- **Limits**: writes of `system.fanout.*` on a figaro or role form, and role
  casts, already pass `refuseCallerKeys` and `fanoutLimitHeld`; the write
  path calls `SetLimits` with the new effective value.

Drift is the risk of any cache. Two defences: the test oracle (every ledger
test ends by comparing the counters to a fresh `Rebuild`), and a
`figaro doctor admission` verb that does the same against the live daemon
and prints any node that disagrees. A rebuild costs half a millisecond, so
the daemon may also rebuild on every topology epoch change.

### Two forks under one capped ancestor, no hierarchy lock

The ledger has one mutex. It is held for the walk (under 2 us at depth 32)
and never across I/O, so a fork waits on another fork's admission for at
most a microsecond or two, never on its 55 ms birth.

- Fork 1 reserves: the King's `used` goes 2 to 3 of 3.
- Fork 2 reserves: it sees 3 of 3 and is refused at once, while fork 1's
  birth is still running.
- If fork 1's birth fails, it aborts and the King goes back to 2. A fork
  that came in between was refused when it need not have been: the ledger
  errs toward refusal, never toward overshoot.

Proven by `TestConcurrentAdmissionHoldsEveryCap`: 16 goroutines, 400 mixed
operations each (reserve, commit, abort, retire, revive, move, re-grant)
under a capped King with a capped and an uncapped lead, 20 rounds under
`-race`; every round ends with no cap exceeded and counters equal to a
rebuild.

A lock-free variant (a compare-and-swap per node, a seqlock against moves)
is possible. At 0.53 us per reservation with 16 goroutines contending on one
cap, it would save nothing measurable and is much harder to prove.

### A reparent in the middle of a fork

A reparent takes `Hierarchy.mu` as today, and inside it the ledger mutex for
its `Move`. A fork holds only the ledger mutex, briefly. Their order:

- **The fork reserved first.** Its pending child is in `used` of its parent,
  and therefore in the parent's charge. If the reparent moves a subtree
  containing that parent, the pending child's charge travels with it and the
  receiver pays for it. When the fork commits, nothing changes; if it
  aborts, the abort walks the parent's current path, the new one.
- **The reparent moved first.** The fork walks the new path and is checked
  against the new ancestors.

There is no third order: both steps are atomic under the ledger mutex, and
the drawn tree the ledger walks is its own copy, changed only under that
mutex. The topology on disk may lag it by one commit; it never disagrees on
an admitted fork, because the ledger changes only when the hierarchy
commits.

## Gluck's optimistic fork

The idea: the fork is born at once, unadmitted; its first provider call
waits on admission; a failed admission tombstones it before any call and
tells the forker.

**Is it needed?** Not on one node. Admission in the ledger costs about a
microsecond; the birth costs 55 ms; a provider call costs seconds. Making
admission asynchronous hides a microsecond behind a design with a visible
failure mode.

**What it costs:**

- A figaro that exists and then vanishes. `figaro ls` and studies of the
  parent show it, `fork -j` prints its id, a script may `send` to it before
  the verdict. Every reader needs an "unadmitted" state or must tolerate a
  ghost.
- The vanishing is a write: a tombstone (or a reap) with its own fsync, a
  libretto release for every study it inherited, and an entry in
  `figaro reaped` that nobody wanted.
- Succession forks the holder and then demotes it under the fork. An
  unadmitted successor would hold the seat before it is known to fit; the
  demote would have to wait on admission anyway.
- The parent's budget is overshot for the life of the ghost, so a second
  fork in that window sees a wrong count and must be judged against
  reservations anyway. That is the ledger again.

**When it wins:** when admission needs something slow and remote. In a
troupe, a capped ancestor may live on another node (a lane forked on Merlin
under a King seated on gluck). Then the check is a round trip, and the
optimistic fork (birth now, gate the first provider call on the remote
verdict) keeps the fork quick. That is a later stage, and the gate belongs
at the provider call, which every figaro already passes through, not in the
fork RPC.

## Could invariants live on a form?

Honestly: the **declarations** already do and should. `max_children`,
`quota` and `mint` are keys on the figaro's form and on role forms; that is
how a seat keeps its cap through succession, and how Gluck sees and edits
them.

The **counters** should not. A form write is an xwal append with an fsync
(14 to 17 ms in the latency lane's numbers), and writes to one form are
ordered on its version. A counter form written by every fork turns into a
global fork lock with an fsync inside it: exactly what Gluck does not want.
The counters are derived state, cheap to rebuild (0.48 ms), and belong in
memory.

What a form can do well:

- **A read-only view.** The ledger publishes a derived form, as the
  libretto does, for `figaro fanout` and for a lead that studies its own
  budget. Written on change, debounced, never read by admission.
- **A fixed vocabulary of invariants.** The ledger knows how to keep
  `max_children`, `quota`, and later `running`; each is a key, and a new
  invariant is new daemon code. Arbitrary rules written on a form ("no more
  than two figaros with outfit X under Y") cannot keep the O(height)
  promise, because the ledger cannot maintain a counter it does not know.

## Revised staged plan (replaces stage 4 of the laws proposal)

| stage | what | breaks on disk | breaks in protocol or behaviour | reverse |
|---|---|---|---|---|
| 4a. the ledger in the fork path | `internal/admission`, rebuilt at start; the fork reserves, births with no hierarchy lock, commits or aborts; `admitFork`'s scan removed; liveness, move and limit hooks; `figaro doctor admission` | nothing | a refused fork's message changes wording; two forks racing for the last slot: the second is refused while the first is still being born, where today it waits 55 ms and is refused. A reparent's fanout check reads the ledger, so a pending fork counts against an arrival | revert; the keys are unchanged |
| 4b. the quota beside `max_children` | `system.fanout.quota` with the corrected charge, re-grant bounds, the succession swap of both keys, labeled mint charged to the minter, the `fan` column and `figaro fanout` | new key only; `max_children` kept | daemon-side succession demotes the predecessor under the successor (as Part 2 proposed); `fork -S system.fanout.quota=` refused past the parent's free budget | unset the key |
| 4c. a ledger view form | derived, read-only, debounced | one derived form | none | drop the form |
| 4d. remote admission, only with troupes | the first provider call of a fork with a remote capped ancestor waits on the verdict; a refused fork is tombstoned before any call | none beyond tombstones | a fork under a remote cap can vanish after its id was printed | turn it off; remote caps go back to honour |

Not proposed: the optimistic fork on a single node.

The birth itself (55 ms, serial on `XwalStore.mu`) is the next lever for
"all forks quick". It is the latency lane's P3 plus a per-trunk store lock,
and needs its own proposal.

## Open for Gluck

1. **Both keys.** `max_children` caps direct live children and `quota` the
   subtree, each checked at every fork, both swapped at succession. Right?
2. **The charge correction.** An uncapped child charges `1 + used`, not
   `1 + live figaros under it`. Without it a grant under an uncapped lead
   escapes the King's quota.
3. **Refusal while a sibling is mid-birth.** The ledger refuses the second
   of two forks racing for the last slot even if the first then fails. The
   alternative, waiting for the first to land, holds a fork up for 55 ms.
   Refuse (proposed) or wait?
4. **Restore past a cap.** Reviving a retired figaro under a full parent:
   refused (proposed, as the prototype does), or allowed for the owner and
   shown as over budget?
5. **The optimistic fork.** Drop it on one node and keep it for troupes
   (stage 4d)?
6. **Next.** Shall the store birth's global lock be the next lane, since it
   is what actually serializes forks?
