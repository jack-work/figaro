# The queue and the turn state, as pushed builtin forms

Commission of 2026-09-09. Worktree `queueform`, branch `feat/queue-form`,
based on `main` @ 2c22b263.

> when sending a message in the transcript tui from `:send --`, upon sending
> it, the message takes some time before it roundtrips the transcript. I need
> the message to be held in the draw until it is roundtripped, and to show a
> status indicator on the client that is animated but distinct from the
> thinking indicator until the client receives the state patch for not
> actively thinking. the state and queue should be put into separate builtin
> forms, and should live-update the client cli using the patch protocol,
> similar to how `form show` does.

---

## 1. The diagnosis

Three complaints, one root cause: **the client's picture of the agent's
disposition is guessed locally and refreshed by polling.**

### 1.1 The thinking spinner is armed on a lie

`livelogTurn.armThinking` (`internal/cli/livelog_bridge.go:303`) sets
`turnStatusThinking` *the instant a submit is accepted: before the prompt has
round-tripped, before the model's first token*. Its own doc comment says so
and is right about why (the footer is a permanent fixture and must not wait on
the stream) and wrong about what to pin: the spinner claims the agent is
thinking about a message that may still be in the socket, may be queued behind
a running turn, and may be refused.

### 1.2 The queue is pulled, twice a second

`startPagerClock` (`internal/cli/stream.go:567`) states the defect outright:

> THE QUEUE is a function of state nobody announces -- a prompt enters it with
> no frame emitted, so a pager that does not ask never learns.

`refreshQueued` (`stream.go:614`) fires a `figaro.queued` RPC on every
`spinnerFPS/queuedPollHz` tick. So an open queue drawer is up to 500 ms stale,
and `commandSend` has to kick a manual `refreshQueued()` after every
`:send` (`internal/cli/command.go:113`) to paper over it. There is no
notification anywhere in the queue's vocabulary; `turn.done`'s `idle` flag is
the whole push side.

### 1.3 A lifted message exists nowhere

`Agent.QueuedPrompts` (`internal/figaro/agent.go:565`) stamps
`rpc.QueueStateQueued` on every row unconditionally. `QueueStateCommitting`
is in the vocabulary (`api/rpc/misc.go:57`) and is never written. And a
message the drain loop has *lifted* is gone from `SnapshotPrompts` — it is
tracked in `Inbox.lifted` (`inbox.go:29`), it is precise enough to power three
distinct refusal reasons (`RejectCommitting` / `RejectCommitted` /
`RejectMerged`), and none of it is published.

**So the interval the author perceives as latency is a hole in the model, not
a slow wire.** Between `MarkCommitted` and the aria frame that carries the new
turn, the message is in neither the queue nor the transcript. Making the wire
faster would not close it. Publishing the state does.

---

## 2. The shape

Two **builtin, non-persistent forms per aria** — facets of the aria, not
entries in the store — carried to the client over the existing form-patch
protocol.

### 2.1 The vehicle: `store.NewMemForm`

`store.MemFormLog` (`internal/store/form.go:501`) already is "a form without
an aria": the patch algebra and the MVCC state with no store under them.
`NewMemForm()` (`form.go:569`) wraps it. From it we inherit, for free:

- minimal patches (`Form.applyEffect` returns `snap.Apply(p).Diff(snap)`, so
  republishing unchanged state emits **nothing** — the identity rule
  `internal/angelus/hubs.go:166` already relies on);
- monotonic versions and `PatchesBetween` for catch-up;
- `OnCommit(fn)` (`form.go:332`) — the exact hook `WatchForm` uses today;
- a client that needs no new code to read them: `formMirror`
  (`internal/cli/form_mirror.go`) and `formView` already fold form deltas and
  already resync on a version gap.

No stump, no xwal node, no lineage, no fork semantics, no durability. They are
born empty at agent construction and die with the agent, which is precisely
the lifetime of the truth they carry. **Nothing on disk changes and nothing is
invalidated** — these forms have never existed, so there is nothing to
migrate.

**One fix owed to `MemFormLog` first.** It appends forever
(`form.go:522` — `next := append(cur, …)`, never trimmed). A status form
patched per round on a week-old aria is an unbounded leak. Give it a floor:
retain the last `memFormLogFloor` records (256 is ample — the client needs
only to survive one read→mutate round trip), drop below, and have
`RangePatches` report a request under the floor as unsatisfiable so
`Form.patchesFromLog` returns `ok=false` and the reader resyncs from the
snapshot. Bounded log + resync-on-gap is strictly better than unbounded, and
it is what makes these forms safe to run for a week.

### 2.2 Addressing: the facet

```
<aria>/queue
<aria>/state
```

`/` is unclaimed in the addressing grammar (`:` is a turn coordinate, `.` an
LT, `@` a form, `#`/`~` unused). So:

- `fig form show 3596d46e/state` works,
- `:form show <id>/queue` works inside the pager,
- `fig queue --watch` becomes a live pit over `<id>/queue` and needs no
  bespoke renderer,

all through `openFormView`, which already does the seed-then-follow dance
correctly (`internal/cli/form_listen.go:22`).

Facets are **not** forms you can mint, fork, bind, or `form ls`. They are a
projection the agent publishes. (Confirm during implementation that no target
resolver splits on `/` today; grep found none.)

### 2.3 `<aria>/state`

```
turn            "idle" | "accepted" | "committing" | "thinking" | "tooling"
turn.id         42
turn.since      1757462400123        -- unix ms of the last transition
turn.reason     "" | "interrupted" | "error: …"    -- last turn's verdict
inflight        3                    -- accepted but not yet answered
epoch           "8f3c…"
model           "claude-…"
```

Written from **one place per transition, on the agent's own loop**, at the
points that already move `turnRunning` and already fan out `turn.done`:

| where | transition |
|---|---|
| `Agent.Qua` accept (`agent.go:556`, after `inbox.Send`) | `→ accepted` |
| `runTurn` entry (`turn.go:160`) | `→ committing` |
| after `MarkCommitted` + `startAssistantUnit` (`turn.go:176`) | `→ thinking` |
| `driveOneRound` round boundary | `thinking ↔ tooling` |
| `finishTurn` (`agent.go:1149`) | `→ idle`, `turn.reason` |

This is a *published view of state the agent already holds*, not new
bookkeeping. `turn.done` stays exactly as it is — its reason string, its
`idle` flag, and `sessionStatus.finishTurn`'s parsing of it are untouched.
The form adds what a one-shot notification structurally cannot: the
**interstitial** states.

### 2.4 `<aria>/queue`

```
epoch           "8f3c…"
len             3
order           [12, 13, 14]
items.12.text   "rebase onto main and push"
items.12.state  "queued"
items.12.at     1757462400123
items.12.sender "gluck"
items.12.merged [10, 11]
items.12.into   0
items.12.turn   0
items.13.…
```

Flat dotted keys are the form's native shape; `buildFormTree`
(`form_mirror.go:92`) renders them as a tree for free. `order` carries FIFO,
because a form is a map. Moving one item's state is **one key**, so the patch
is tens of bytes.

Written from **one place: the inbox.** Every enqueue, mutation, lift, commit,
coalesce and drain already passes through `Inbox`; a publisher anywhere else
can disagree with the queue's own state. Concretely, give `Inbox` a
`publish func(queueProjection)` sink set at construction and fire it at the
tail of `Send`, `Recv`/`TakeReadyUserPrompts` (lift), `MarkCommitted`,
`Prepend`, `DeletePrompts`, `UpdatePrompt`, `CoalesceUserPromptRuns` and
`DrainUserPrompts`. The sink projects to the flat key set and submits; `Diff`
makes an unchanged republish free, so the call sites need no cleverness about
whether anything moved.

**`QueueState` widens so an item never blinks out of existence:**

| state | means | source of truth today |
|---|---|---|
| `queued` | in the inbox, deletable | the FIFO |
| `committing` | lifted by the drain loop, not yet in the IR | `Inbox.lifted` |
| `merged` | folded into another id by an interrupt (`into: N`) | `event.merged` |
| `committed` | it is a message now (`turn: N`) | `Inbox.committed` |
| `dropped` | deleted by the user | `DeletePrompts` |
| `drained` | cleared by a hangup | `DrainUserPrompts` |

Every one of these facts is already tracked — `refuseLocked`
(`inbox.go:349`) can already distinguish all six. They were simply never
published.

**Retention is what "held in the draw until it is roundtripped" means.** A
`committed` item stays in the form, bounded by `committedRing` (64, already
the ring size for exactly this "outlive a client round trip" reason), carrying
the turn id it became. The client drops the row when *its own* transcript
adopts that turn — which it knows, and which the form must not have to know
per-client. That closes §1.3's hole with a retention policy rather than a
client-side timer.

### 2.5 Transport

`rpc.FormDelta` gains one field:

```go
type FormDelta struct {
	Schema  int       `json:"schema"`
	AriaID  string    `json:"aria_id,omitempty"`
	Facet   string    `json:"facet,omitempty"`  // "" == the bound board
	Version uint64    `json:"version"`
	Patch   FormPatch `json:"patch"`
	At      int64     `json:"at,omitempty"`
}
```

`""` means the board, so every existing client, every existing test and every
recorded tape keeps its exact meaning. **`FormDeltaSchema` does not bump** —
the mirror's schema check exists for shapes a client *cannot read*, and an
added `omitempty` field is not one. (If a tape asserts on the delta's literal
JSON it gets a new key and must be re-recorded; check `internal/tape`.)

Agent side: two more `OnCommit` sinks beside the `WatchForm` one at
`agent.go:279`, each fanning out with its facet stamped. Same socket, same
fanout, same ordering. **No new connection and no subscribe request** — every
accepted aria connection is already registered for notifications.

Read side: `figaro.form` takes an optional `facet` so a mirror can seed and
resync. That is the entire server surface addition.

### 2.6 The client

- `notifyHandler` (`command.go:316`) grows `case rpc.MethodFormDelta:`,
  routing by facet into one of two session-owned `formMirror`s.
- **The poll dies.** The `n%every` queue fetch in `startPagerClock` goes;
  `refreshQueued` survives only as the resync path a mirror gap calls, and
  `commandSend`'s manual kick (`command.go:113`) goes with it.
  `setTranscriptQueued` becomes a projection of the queue mirror rather than
  an RPC result. Because the mirror is pushed and authoritative, `tr.queued`
  is current whether the drawer is open or not — and
  `updateQueuePitRows` (`transcript.go:1497`) already replaces rows in place
  with cursor preservation, so **the open drawer simply stays live**. That is
  the whole of the "keep the queue current even when the draw is open"
  requirement; the machinery was already there and was starved of events.
- **The status indicator.** `sessionStatus.turn` stops being guessed.
  `turnStatusThinking` becomes settable *only* by a state-form patch. Two
  states join it:

  | state | glyph family | life |
  |---|---|---|
  | `turnStatusSending` | a **departure**: `→ ⇢ ⇉ ⇶` | set locally at submit, before the RPC returns. "I have spoken and nothing has confirmed it." |
  | `turnStatusAccepted` | a **holding orbit**: `⠁⠂⠄⡀⢀⠠⠐⠈` | the daemon has it and is not yet answering it (state `accepted`/`committing`, or the queue holds it behind a running turn) |

  Both animate. Both are a different *family* from `livedoc.SpinnerFrames`,
  not a different speed of it — a reader tells families apart at a glance and
  speeds apart never. `armThinking` becomes `armSending`: it still pins the
  footer at submit (that requirement stands and is correct) but it pins the
  honest state, and the handover to `thinking` happens on the patch. If the
  state form never arrives, the bar sits in `sending`, which is *true*.
- `sessionStatus.advance()` (`session_status.go:289`) currently animates only
  `turnStatusThinking`; it must animate the whole moving set, and
  `turnRunning()` (`:304`) — which decides exit 130 vs a clean close — must
  count the new states as in-flight.

---

## 3. Staging

Each stage is shippable and testable on its own.

1. **`MemFormLog` floor; `FormDelta.Facet`.** Store + rpc only. Tests: the
   ring drops below the floor; `PatchesBetween` under the floor reports
   unsatisfiable rather than lying; a faceted delta round-trips the wire.
2. **`<aria>/state`, published from the turn loop.** Agent only, no client
   change. Test: drive a turn against a mem form and assert the *sequence* of
   states and versions; assert `finishTurn` publishes `idle` + reason; assert
   an unchanged republish emits no delta.
3. **The queue projection, published from the inbox**, with the widened
   `QueueState`. Agent only. Tests beside `queue_crud_test.go` /
   `queue_drain_test.go`: enqueue→lift→commit is three patches and **never a
   hole**; a coalescing interrupt marks `merged` with `into`; a delete marks
   `dropped`; a clearing hangup marks `drained`.
4. **Client: two mirrors, routing, delete the poll.** The drawer becomes
   reactive. Painting change ⇒ **tmux, real pty, per the `tmux-testing`
   skill**. A passing unit test proves nothing here.
5. **The two new turn states and their animation.** tmux *and* a tape
   (`debugging/tapes.md`) — this is exactly the class of bug the tape
   machinery exists for.
6. **Surface it.** `fig queue --watch`, `fig form show <id>/state` in the CLI
   table; the facet in `reference/forms.md` and `reference/ui-stream.md`;
   `WellKnownKeys` gets the facet key catalog so completion knows them.

---

## 4. What this obsoletes

`proposals/status-queue-proposal.md` §2.3 proposed a bespoke `figaro.queue`
notification with its own `{epoch, v, len, ops}` envelope, its own monotonic
version, its own gap rule and its own resync-by-read. **That should be
deleted, not implemented.** It is a fourth delta shape reimplementing
versioned-suffix-with-resync-on-gap, which `form.delta` + `formMirror`
already do and are already tested.

Its §2.4 argued "a queue entry is not a node and should not pretend to be
one". Correct — and the conclusion is that a queue entry is a **form**, not
that it needs a new protocol. Stages 1–3 of that proposal landed (the queue
hangs under the status row, `Q` walks it) and stand unchanged. Stage 4 is
superseded; rewrite the file rather than leave two live designs in the tree.

---

## 5. Open questions

1. **The indicator's meaning.** I read "animated but distinct … until the
   client receives the state patch for not actively thinking" as: the distinct
   indicator runs from submit until *authoritative* state arrives, after which
   the bar follows the form (thinking spinner while thinking, retire on idle).
   The other reading is that the distinct indicator persists all the way to
   idle and the thinking spinner never shows for a message that was queued.
   Which?
2. **Committed-item retention.** By count (`committedRing`), by time (~2 s),
   or by client acknowledgement? Acknowledgement is the only *correct* one but
   puts a per-client fact into a form every client shares. My inclination is
   §2.4's split: retain by count in the form, and let the client drop the row
   when its own transcript adopts the turn.
3. **Facet spelling.** `<id>/queue`, or something else. And: should facets
   appear in `fig form ls`? I say no.
4. **Should `/state` absorb the metrics** that `seedMetrics`
   (`command.go:296`) polls every fourth tick with a backward read of one
   message? Same disease, same cure. Out of scope here, but it is the obvious
   next patient, and if the answer is yes the key set should leave room now.
