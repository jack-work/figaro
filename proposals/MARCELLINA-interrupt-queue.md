# MARCELLINA — interrupt / cancel control flow and queue semantics

Design note for `feat/interrupt-queue` (forked from main @ e6f52c9). Sections
1–3 are the three points review pushed hardest on; §4 is the wire; §5 is
item-by-item; §6 names the conflicts; §7 is the commit plan.

**Status: REVIEWED AND RULED ON** by 45ea3c58. Three rulings and two new
required items are folded in below, marked ▸RULING / ▸REQUIRED. Where a ruling
contradicts my original proposal the original is withdrawn and the ruling
stands; §8 is the ledger.

---

## 0. How the queue is represented today

**The queue is the agent inbox** — `internal/figaro/inbox.go`, one per aria: a
mutex+cond FIFO of `event` (`internal/figaro/agent.go`):

```go
type event struct {
    typ        eventType     // eventUserPrompt | eventSet | eventFork
    text       string
    chalkboard *rpc.ChalkboardInput
    setPatch   message.Patch
    fork       func() error; forkDone chan error
}
```

- **Events have no identity** — no id, no timestamp. `figaro.queued` returns
  bare strings (`Inbox.SnapshotUserPrompts`, which also *drops* empty-text
  prompts, i.e. pure chalkboard carriers). Item 5 needs ids before anything.
- **Three takers, all prefix-only** — `TakeReadyUserPrompts`, `TakeReadyForks`,
  `TakeReadySet` lift only the contiguous prefix run of their own kind, so FIFO
  order *across* kinds is never violated. `Recv` pops one and blocks.
- **Two drain regimes.** Idle: `act()` → `Recv()` → `runTurn(evt)` — one event,
  one turn. Mid-turn: `prepareProviderRound()` / `appendSteeringPrompts()` →
  `TakeReadyUserPrompts()` → `mergePromptEvents()` → **one message** →
  `appendUserPrompt(…, steering=true)`.
- `Prepend` restores a batch that failed to persist; `IsIdle()` drives
  `state: active|idle` and `turn.done{idle}`.

---

## 1. ITEM 1 — coalescing: the exact path, and why a normal submit cannot reach it

### 1.1 Where interrupt enters, today

```
Ctrl-C (cli/stream.go inputInterrupt → ctx cancel → the ctx.Done branch)
figaro hup           (cli/hup.go runHup)
        │
        └─► figaro.Client.Interrupt          internal/figaro/client.go
            └─► "figaro.interrupt"           internal/figaro/server.go Handle
                                             (runs on an RPC goroutine, NOT the drain loop)
                └─► Agent.Interrupt()        internal/figaro/agent.go
                        a.mu.Lock()
                        if a.turnCancel == nil { unlock; return }   // idle: no-op today
                        a.interrupted = true
                        cancel := a.turnCancel
                        a.mu.Unlock()
                        cancel()                                    // turnCtx dies
```

`Agent.Interrupt` **does not touch the inbox**. That is exactly why `act()`
afterwards pops the queued prompts one at a time, each opening its own turn.

### 1.2 The proposed path — ▸RULING 1: coalesce each maximal RUN, not the whole queue

```
Agent.Interrupt(disposition)
    a.mu.Lock()
    running := a.turnCancel != nil
    if running { a.interrupted = true; cancel := a.turnCancel }
    a.mu.Unlock()
    if running { a.inbox.CoalesceUserPromptRuns() }   ◄── NEW. The ONLY call site.
    if running { cancel() }
```

**Where the coalesced fold happens: `Inbox.CoalesceUserPromptRuns()`** — a new
method on `Inbox`, one locked pass, atomic against every other inbox operation
(`Send`, the three takers, `Prepend`, and the new CRUD mutators all take `b.mu`):

```go
// CoalesceUserPromptRuns folds each CONTIGUOUS RUN of queued user prompts into
// one event parked at that run's first position, keeping its id. A queued set
// or fork is a BARRIER: it is never crossed, and every event keeps its
// relative order. Called from Agent.Interrupt and nowhere else.
func (b *Inbox) CoalesceUserPromptRuns() {
    b.mu.Lock(); defer b.mu.Unlock()
    // walk the queue; replace each maximal run of eventUserPrompt with one
    // mergePromptEvents fold of it.
}
```

The barrier is the point. All three existing takers are prefix-only precisely
so FIFO across event kinds is never violated; folding a prompt past a queued
`set` would be the only place in the tree that reorders across kinds, and it
would answer that prompt against a chalkboard it was never written against — no
error, no log line, nothing to notice. Across a `fork` it is worse (wrong
trunk). And in the gesture the requirement describes — a human with three typed
messages hitting Ctrl-C — `set`/`fork` arrive by CLI, not the composer, so
there is no interleaved control event and run-coalescing **is** whole-queue
coalescing. Identical on the intended case, correct on the unintended one.

**Idle is not the interrupt path.** With no turn in flight there is nothing to
interrupt and the drain loop is already working the queue; folding then would
change normal-submit semantics. `Interrupt` keeps its early return and
coalesces nothing. (`cut`'s clear is independent of a running turn — §4.2.)

**The race review named: a prompt arriving between the fold and the drain.**
It is a separate event and is **not** retroactively folded. It then behaves
like any other prompt, by the rule that already exists — the drain classifies
it at the boundary: a steer if the combined message's turn is already running,
its own turn if it lands after that turn ends. Deterministic, and pinned.

### 1.3 Why a normal submit cannot reach it — by construction

It is a **separate entry point**, not a mode:

| path | entry point | can coalesce? |
|---|---|---|
| `figaro.qua` → `Agent.SubmitPrompt` | `Inbox.Send` | no such code |
| drain, mid-turn steer | `Inbox.TakeReadyUserPrompts` | no such code |
| durability retry | `Inbox.Prepend` | no such code |
| **`figaro.interrupt` → `Agent.Interrupt`** | **`Inbox.CoalesceUserPromptRuns`** | **the only one** |

There is **no boolean threaded anywhere** and no `if interrupting` inside a
shared mutator. `Agent.Interrupt` is itself the interrupt path — a function
with one meaning — so the guard is the call graph, provable by
`grep -r CoalesceUserPromptRuns` returning exactly two hits (definition + the
call in `Interrupt`). Pinned by tests: `Send`×3 → `Recv` yields three events;
`Send`×3 → `Interrupt` → `Recv` yields one; and — the executable form of
ruling 1 — `Send(p1) Send(p2) Set Send(p3)` → `Interrupt` yields
`[merged(p1,p2), set, p3]`: **three** events, not one.

**What IS shared, deliberately: the merge itself.** `mergePromptEvents([]event)
(event, bool)` (`turn.go`, unchanged) — texts joined by `\n` in queue order,
chalkboard inputs merged so a later value wins (`mergeChalkboardInput`). It is
a **pure function with no notion of interrupt**, and it is the whole of "the
same semantics steering already has". Sharing a pure fold is not sharing a
mode.

**And the drain loop cannot tell.** After the fold, the queue is an ordinary
queue holding one ordinary prompt; `Recv` → `runTurn` → `appendUserPrompt(…,
steering=false)` runs the identical code an idle aria runs for a single
multi-line message. No branch downstream asks "was there an interrupt".

---

## 2. ITEM 4 — turn legality (leading with the invariants, not the truncation)

### 2.1 What makes a turn LEGAL in the store/IR today

The IR is an append-only figwal log of `message.Message`
(`internal/message/message.go`). Legality is not one property; it is these,
and every one has a named reader that depends on it:

| # | Invariant | Reader that depends on it | Failure mode |
|---|---|---|---|
| L1 | **Tool pairing.** Every `ContentToolInvoke` block in a `RoleOutput` message is answered by a `ContentToolResult` block with the same `ToolCallID` in a LATER message. | `anthropicsdk/encode.go renderMessage` → `anthropic.NewToolUseBlock`; the API itself | HTTP 400, every subsequent turn |
| L2 | **No dangling tail.** The log tail is never a `RoleOutput` carrying unanswered invokes. | `repair.go repairInterruptedTail` (boot + top of `runTurn`) | boot-time synthetic repair with `tailRepairNotice` — output silently lost |
| L3 | **Only fully-decoded calls persist.** A `ContentToolInvoke` reaching the IR has `ToolCallID`, `ToolName` and non-nil `Arguments`. | `turn_repair.go partialAssistant` (filters), `encode.go toolInput` | a `tool_use` with `{}` input that L1 then obliges us to answer |
| L4 | **One assistant append per round.** Predicted `assistantIdx = a.nextIndex()` must equal the append's `LT`/`FigaroLT`. | `driveOneRound`'s explicit check; `deferredAppendLog` (the provider's writes are staged, the drain loop performs the real append) | `"assistant append LT mismatch"`, turn aborted |
| L5 | **TurnID agrees with `turns.StampIDs`.** `appendMsg` stamps `a.turnID`; `StampIDs` re-derives ids purely from roles/steering/text. | `turns.Opens`/`StampIDs`, the aria projection | live turn id runs ahead of the projected one; the client sees a new turn and abandons the one on screen mid-stream |
| L6 | **A repair tic must not OPEN a turn.** `turns.Opens` = `RoleInput && !IsSteering && Text != ""`, and `IsSteering` is true for any message with a tool_result (`HasToolResult`). | `turns.Opens` | a synthetic close would silently start turn N+1 |
| L7 | **No open live unit at rest.** `ariaSrv` is `Close`d (unit reached the IR) or `Abandon`ed (it did not), then `Seal`ed. | `aria.Server`, `endTurn` vs `endTurnDiscarding` | a UI message the IR lacks → the next turn regenerates it → visible duplicate |
| L8 | **No unsigned thinking on the wire.** The IR has no home for an Anthropic thinking *signature*; the signed form lives only in `translations/<provider>/…` keyed by `FigaroLT`. | `encode.go renderMessage` `case ContentThinking:` — **dropped** on the cache-miss path | HTTP 400 |

### 2.2 What a mid-flight truncation violates

Cancelling `turnCtx` at an arbitrary instant breaks, if nothing else is done:

- **L1/L2** — the provider may already have appended a `RoleOutput` with tool
  invokes whose tools were killed by the cancel (`killProcessGroup` on the
  bash tool's process group). Dangling.
- **L3** — the stream may be mid `input_json_delta`: `asm.toolOpen` has created
  a `ContentToolInvoke` with a name and *no* arguments.
- **L4** — the provider goroutine and the drain loop can both be mid-append.
- **L7** — a live unit is open by definition (we are streaming into it).
- **L8** — a truncated assistant is appended by *us*, so no translation-cache
  row exists for its LT.

### 2.3 How the message is closed so a new one is legal

All of it already exists; item 4 adds no store machinery. In `driveOneRound`,
each `a.isInterrupted()` check routes to `repairTurnTail()`
(`turn_repair.go`), which reads the `turnState` that `noteAssistant` /
`noteTool` have been accumulating all round:

- **L4 first.** `t.committed` says whether the provider's assistant already
  landed. If it did, we append nothing of it; if it did not, we append
  `t.assistant` ourselves. Exactly one append, either way.
- **L3.** `partialAssistant` copies content but **drops** any
  `ContentToolInvoke` missing id/name/arguments — the half-decoded call never
  reaches disk, so nothing owes it a result.
- **Truncation is marked, not hidden.** The appended assistant carries
  `StopReason = message.StopAborted` (`"aborted"`).
- **L1/L2.** `interruptedToolResults(t.tools)` emits one
  `message.ToolResultContent(ToolCallID, ToolName, text, isErr)` block per
  tool the assistant called, on a **single `RoleInput` message** — see §2.4 for
  what `text` is. Tail is now `RoleInput`; `repairInterruptedTail` no-ops.
- **L6.** That message carries tool_result blocks, so `turns.IsSteering` →
  `turns.Opens` is false. The truncated turn stays ONE turn; the next prompt
  opens the next one.
- **L7.** `endTurn("interrupted")` → `emitCommit` (`ariaSrv.Close`) →
  `finishTurn` → `ariaSrv.Seal(nil)` + `turn.done{reason:"interrupted", idle}`.
  When the partial never reached the IR the path is `endTurnDiscarding` →
  `abandonLive`.
- **L8.** The repaired assistant gets **no** `commitAssistantCache` row, so the
  next `Send` takes the cache-miss path, which drops thinking blocks rather
  than replay unsigned ones.

`runTurn` then returns and `act()` is back on `Recv()` — the aria is receiving
again, with `turn.done{idle}` telling the client whether a queue remains.

### 2.4 What an in-flight tool's `tool_result` becomes

**A synthesized error `tool_result`, never an elision.**
`interruptedToolResults` (`turn_repair.go`) per tool:

| tool state at the cancel | `text` | `is_error` |
|---|---|---|
| finished (`ok`/`error`) before the cancel landed | its real `Result` | its real status |
| finished but only the streamed tail was recorded | tail + `"\n\n[output truncated: process interrupted before the full result was recorded]"` | as recorded |
| still running | `boundedToolTail(OutputTail)` (≤ `liveOutputTail`=200 lines, ≤64 KiB) + `"\n\n"` + `interruptedToolNotice` = `"interrupted: tool execution did not complete"` | **true** |

So the partial output the user watched scroll by is *kept*, bounded, and
labelled. On the wire it is `anthropic.NewToolResultBlock(id, text, isErr)`;
`renderMessage` substitutes `"(empty)"` for an empty text, so an empty block
is impossible.

**Would any existing reader choke?** Walked, one by one:

- `anthropicsdk/encode.go renderMessage` — `case ContentToolResult` → a
  `tool_result` block. Fine. Role alternation is repaired downstream by
  `assemble.go coalesceMessages`.
- `provider/anthropic` and `provider/copilot` — same block shape.
- `compose`/projector → `livedoc` tool node with `Status: "error"`. Fine.
- `message.IsCeremonial` — a message carrying `ContentToolResult` returns
  false, so it counts as a real message. Correct.
- `turns.Opens`/`StampIDs` — L6 above.
- `store/meta_heal`, `tokens.EstimateMessage` — content-agnostic.

**One thing I am flagging rather than using.** `message.RoleSystemInterrupt` +
`ContentInterrupt` + `NewInterruptSentinel` exist and are translated by all
three providers into synthetic tool_results (`encode.go` case at line 85) —
but **`NewInterruptSentinel` has no non-test caller**. There are two designed
mechanisms for closing dangling calls and only the real-`tool_result` one is
live. I propose to keep using the live one (it carries the partial output;
the sentinel discards it) and to say so in the docs, so a future reader does
not mistake the dormant sentinel for the mechanism. Deleting it is out of
scope for this branch.

### 2.5 What item 4 therefore actually adds

A keymap row and an RPC call. `inputHangUp` fires
`figaro.interrupt {"queue":"keep"}` and returns `keyHandled` — the *whole*
difference from Ctrl-C, which returns `keyStop` and exits 130.

**Proof obligation, not assertion.** A test in `internal/figaro`: interrupt
with a tool in flight, then `Qua` a fresh prompt on the same aria and assert
(i) it is answered, (ii) `repairInterruptedTail` finds nothing to do, (iii)
every `tool_use` in the log has a matching `tool_result`, (iv) `StampIDs` over
the final log agrees with the stamped `TurnID`s (L5). Plus a real-pty tmux run
for the key, per the tmux-testing skill.

▸**RULING — that is not enough. Three additions, all required:**

1. **Legality must survive a RELOAD.** Interrupt mid-tool, then drop the agent
   and re-open the aria from disk before sending the next prompt.
   `repairInterruptedTail` runs at boot for exactly this reason; a legality that
   holds only in a warm agent is not legality. L2 is the invariant under test.
2. **Assert on the PROVIDER REQUEST, not only the IR.** Different path,
   different failure mode: the IR can be well-formed while the built request
   carries an unmatched `tool_use`, because IR→wire is `renderMessage` +
   `coalesceMessages` + the translation cache and each of the three can drop or
   merge a block. The assertion is on the assembled `[]anthropic.MessageParam`:
   every `tool_use` id in an assistant param is answered by a `tool_result`
   block carrying that id in the next user param.
3. **The coalesce/drain race is stated and tested** — §1.2's last paragraph.

---

## 3. ITEM 5 — rejected deletes are a value, not an error

```go
// QueueOutcome is what happened to ONE requested mutation. There is no
// "ok bool" anywhere on the response: reading the outcome is the only way to
// learn anything, so it cannot be skipped and defaulted.
type QueueOutcome string

const (
    QueueDeleted  QueueOutcome = "deleted"
    QueueUpdated  QueueOutcome = "updated"
    QueueRejected QueueOutcome = "rejected"
)

// QueueRejection is the closed set of reasons a mutation was refused. A
// refusal is a legitimate server decision, so it travels as data.
type QueueRejection string

const (
    RejectCommitting QueueRejection = "committing" // lifted by the drain loop this instant
    RejectCommitted  QueueRejection = "committed"  // already appended to the IR by the running turn
    RejectMerged     QueueRejection = "merged"     // folded into another message by an interrupt (§6-B)
    RejectStale      QueueRejection = "stale"      // the epoch is from a previous inbox generation (§4.1)
    RejectUnknown    QueueRejection = "unknown"    // never seen, or long since answered
    RejectClosed     QueueRejection = "closed"     // the inbox is shut
)

// QueueResult is one requested id's fate. Reason is set iff Outcome is
// QueueRejected; Detail is human prose ("committed to turn 7"), never parsed.
type QueueResult struct {
    ID      uint64         `json:"id"`
    Outcome QueueOutcome   `json:"outcome"`
    Reason  QueueRejection `json:"reason,omitempty"`
    Detail  string         `json:"detail,omitempty"`
    Into    uint64         `json:"into,omitempty"` // RejectMerged: the surviving id
}

type QueueDeleteResponse struct { Results []QueueResult `json:"results"` }
type QueueUpdateResponse struct { Result  QueueResult   `json:"result"`  }
```

The RPC **succeeds** — JSON-RPC `error` stays reserved for transport and
malformed-request faults. The Go client returns `([]rpc.QueueResult, error)`
where `error` can only be a transport fault. Go cannot force a caller to read
a struct field, so the design does the next best two things: the response
carries **no summary flag to mistake for success**, and a non-empty request
answered with an empty `Results` is a protocol violation the client reports
loudly. The CLI derives its exit code from the results (0 = all applied, 1 =
any rejected) and prints every rejection with its reason.

Precision of `committed` over `unknown` costs one small thing: the agent keeps
the ids it drained for the life of the turn (`a.inflight []uint64`, cleared at
`finishTurn`), so "I already answered that" is distinguishable from "I have
never heard of that". Marking an id `committing` happens **inside** the same
locked pass as `TakeReadyUserPrompts`, so a delete either wins the race or is
told the truth.

---

## 4. Wire surface (JSON-RPC, per-aria socket)

### 4.1 Identity — ▸REQUIRED ITEM A: ids must not be reusable across a restart

Review is right and I had it wrong. A bare per-inbox counter resets whenever an
`Agent` is constructed — a daemon restart, but also any dormant→attach — so a
client holding `id 3` from before could delete a *different* message that is now
`id 3`. Silent wrong-message deletion. The counter is not the bug; the missing
generation is.

**Mechanism chosen: an equality-only boot nonce, used as a compare-and-swap
token.**

- Each `Agent` mints `queueEpoch` once at construction: 8 bytes of
  `crypto/rand`, 16 hex chars. **A random nonce — not a clock, not the log
  tail.** The only operation ever performed on an epoch is *equality*, so
  ordering buys nothing, and both ordered candidates are unsound here:
  wall-clock can go backwards, and the figwal tail LT does **not** advance when
  an agent boots, queues, and dies without appending — which is precisely the
  case that reproduces a colliding id.
- `event` gains `id uint64` (per-instance counter) + `at int64`.
- Every mutation request carries the epoch it was read against. A mismatch is
  rejected with `reason: "stale"`; **nothing is mutated**.
- The CLI hides it: `figaro queue rm 3` reads the queue first, takes the epoch
  from that snapshot, then mutates. A stale id can therefore never delete the
  wrong message — it is *told* it is stale.
- `--all` names no id, so it needs no epoch; the epoch is required only when
  `ids` is non-empty. An id is only meaningful within its generation.
- Epoch is a **string** on the wire: a random uint64 exceeds 2^53 and would
  lose precision in `jq` and in every JS consumer.

```go
type QueuedPrompt struct {   // extended in place; the wire field stays `prompts`
    Epoch  string   `json:"epoch"`            // inbox generation; equality-only
    ID     uint64   `json:"id"`               // seq within that generation
    Text   string   `json:"text"`
    State  string   `json:"state"`            // "queued" | "committing"
    At     int64    `json:"at,omitempty"`
    Merged []uint64 `json:"merged,omitempty"` // ids folded in by an interrupt
    Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"` // drained payloads only
}
```

`RejectStale` joins the closed reason set of §3.

### 4.2 `figaro.interrupt` — explicit queue disposition

```jsonc
{"queue": "keep"}    // default when absent — today's behaviour
{"queue": "clear"}
→ {"ok": true, "cleared": false, "queue": [QueuedPrompt, …]}
```

`queue` = **the queue as of the hangup**, one meaning always; `cleared` says
whether those messages were removed. `clear` works when the aria is idle too.
An empty request from an older client behaves exactly as today.

### 4.3 CRUD

- **C** — `figaro.qua`. No second create path: a queued message *is* a
  submitted prompt.
- **R** — `figaro.queued`. ▸**REQUIRED ITEM B: additive, not broadened.** My
  original proposal changed what an existing method *returns* for every existing
  consumer (listing the empty-text chalkboard carriers `SnapshotUserPrompts`
  filters out today). Withdrawn. The filter stays exactly as it is; carriers
  come back only behind an explicit opt-in, `{"include_carriers": true}`, which
  only `figaro queue` sets — because only CRUD needs to address them. The `Q`
  panel and the inline trailer keep today's output byte for byte. The element
  type gains fields (`epoch`, `id`, `state`, `at`, `merged`); that is purely
  additive — an existing consumer reading `.text` is untouched — and it is
  called out in the docs commit regardless.
- **U** — `figaro.queue.update {"epoch":"…","id":N,"text":"…"}` →
  `QueueUpdateResponse`.
- **D** — `figaro.queue.delete {"epoch":"…","ids":[…]}` | `{"all":true}` →
  `QueueDeleteResponse`.

---

## 5. Item by item

**1 — coalesce on interrupt.** §1. Ships with the id/`Merged` metadata of §4.1.

**2 — hangup that CLEARS.** `figaro cut [<id>] [-j]` →
`figaro.interrupt {"queue":"clear"}`. Plain: `cut <id> — interrupted, N queued
messages drained`. `-j`: one line,
`{"aria":…,"interrupted":true,"cleared":true,"queue":[…]}` — the drained
messages verbatim, text + chalkboard input, so `figaro cut -j > lost.json` is a
lossless save. (*Naming:* `cut` = cut the line, against `hup` = hang it up
politely. Fallback if it reads wrong beside `fork`/`kill`: `figaro hup --clear`,
which costs the two-verb clarity you asked for.)

**3 — hangup that does NOT drain.** CLI `figaro hup [<id>] [-j]` →
`{"queue":"keep"}` (today's behaviour, plus JSON). TUI: the key below.

▸**RULING — the disposition must be unmissable in BOTH help lines, and each
verb must name the other.** The flag is not where the meaning lives:

```
hup  — Hang up: stop the turn, KEEP queued messages (`cut` discards them)
cut  — Hang up and DISCARD queued messages; -j returns them (`hup` keeps them)
```

**4 — transcript hotkey.** §2. New keymap row, `modeTranscript | modePanel`,
`open: staysInline`, chord `'H'`, `input: inputHangUp`, `help: helpHangUp`.
Input-level because it owns an RPC, not the viewport. `'H'`/`'h'` are both
free today.

**5 — CRUD.** §3 + §4.3, plus a `figaro queue` verb group:

| Command | Effect |
|---|---|
| `figaro queue [<id>] [-j]` | List: id, state, age, text. |
| `figaro queue rm <id>… [-j]` | Request deletion; one line per id with its outcome. |
| `figaro queue rm --all [-j]` | Same, whole queue. |
| `figaro queue edit <id> -- <text>` | Replace a queued message's text. |

Exit codes: 0 all applied, 1 any rejected, 2 argv. The transcript `Q` panel
grows an id column so the ids are visible where the queue is.

**6 — hang up and stay listening.** The same key as item 4 (see §6-E): help row
`H — hang up: stop the turn, keep listening`; a `turnStatus` token
`hung up · listening` painted through `sessionStatus.setNotice`/`setTurn`
(never written straight to the terminal while the pager owns the screen). The
pager already has listen semantics past `turn.done`, so no session plumbing
changes.

**7 — reconcile with main** before handoff.

---

## 6. Conflicts and ill-posed corners — on paper, not split

**A. Item 1 vs item 2 (coalesce vs clear).** *(Unaffected by ruling 1: `clear`
still never coalesces.)* If `cut` coalesced and *then*
drained, the JSON you keep would be one merged blob instead of the N messages
the user actually typed — directly against item 2's purpose ("persisted rather
than lost"). **Resolution:** the two dispositions are exclusive and ordered.
`clear` drains the N originals verbatim, with their own ids, and never
coalesces; `keep` coalesces. One enum on the wire, one branch in
`Agent.Interrupt`, no interaction.

**B. Item 1 vs item 5 (coalescing destroys ids).** A panel showing ids 4,5,6
when an interrupt lands loses 5 and 6. Answering a later `queue rm 5` with
`unknown` would be a lie. **Resolution:** rejection reason `merged` with
`into: 4`, and `Merged:[5,6]` on the survivor, so the mapping is discoverable
in both directions. Deleting 4 deletes all three, and the CLI says so.

**C. Item 1 vs item 3 (what does `hup -j` print?).** Post-coalesce: one message
with `merged:[…]`, because that is what will actually be answered. The
verbatim N are available from `figaro queue -j` *before* hanging up, or from
`cut -j`. Stating it so it is not discovered.

**D. Item 4's "may pause for an in-flight tool call" — genuinely ambiguous.**
Two readings: **(i)** the *gesture* may take a moment (the CLI shows
`hanging up…` until `turn.done`) — costs nothing, already true today; **(ii)**
the interrupt should let the running tool *finish* and record its real result.
(ii) is a behaviour change: a 30-minute `bash` would hold the hangup, which
contradicts Ctrl-C's current promptness and the process-group kill. **I plan
(i)** and want your ruling before building (ii); if you want (ii) it should be
its own verb (`hup --after-tool`), never a hidden mode of this one.

**E. Item 4 vs item 6 are one gesture.** "Transcript hotkey that stops the
conversation" and "UI affordance: hang up and stay listening" describe the same
binding from two angles. I collapse them into one key + one affordance rather
than invent two. Corollary worth stating: there is **no incipit-mode
equivalent**, because an incipit session closes on `turn.done` by design —
which is consistent with item 4 saying *transcript* mode.

**F. `figaro.queued` currently hides empty-text prompts.** Listing them (needed
for CRUD — they are deletable) is a small behaviour change to the `Q` panel and
the inline trailer; both filter client-side instead, so nothing renders a blank
row.

**G. `-j` shape.** cli.md's contract is "one machine-readable line". `hup`/
`cut`/`queue` `-j` print **one JSON object** with the queue inline, not NDJSON
per message.

---

## 7. Commit plan

1. `rpc`: queue wire types, interrupt disposition, round-trip tests.
2. `figaro/inbox`: ids + timestamps + **the epoch of §4.1 (required item A —
   folded in here per ruling: inbox identity, not a follow-up)**, richer
   snapshot, in-flight id tracking.
3. `figaro`: **`Inbox.CoalesceUserPromptRuns` + the one call in
   `Agent.Interrupt`** (item 1) + tests — **does not land without the test that
   a queued `set` BLOCKS coalescing**, the executable form of ruling 1.
4. `figaro`: `queue:"clear"` drains and returns (item 2, server).
5. `figaro`: `figaro.queue.update` / `.delete` with `QueueResult` outcomes (item 5, server).
6. `cli`: `hup -j` (keep) and `cut` (clear) (items 2, 3).
7. `cli`: `figaro queue` group (item 5, client).
8. `cli`: `H` hang-up-and-stay-listening key, help row, status token (items 4, 6),
   verified in a real pty per the tmux-testing skill.
9. `figaro`: the legality test of §2.5 (interrupt mid-tool, then prompt again).
10. docs: `skills/figaro/cli.md`, `skills/figaro/reference/ui-stream.md`, keymap help.
11. reconcile with `main` (item 7).

Green gate on every commit: `go build ./... && go vet ./... && go test ./...`.
Iteration in `nix develop` in this worktree; scratch builds on `/var/tmp`.
Test runs bounded and attended (`systemd-run --user --scope -p MemoryMax=4G -p
TasksMax=256 --setenv=FIGARO_NO_SELF_SPAWN=1`), every fix canaried with
`-count=1`, and commit 8 read off a real pty on a private tmux socket.

---

## 8. Ruling ledger

| # | Ruling | Effect |
|---|---|---|
| 1 | Coalesce maximal prompt RUNS; `set`/`fork` are barriers | §1.2 rewritten, whole-queue form withdrawn; barrier test gates commit 3; docs say it in those words |
| 2 | `hup`/`cut` stand; disposition unmissable in both help lines, each names the other | §5 item 3 |
| 3 | Item 5 outcome type approved as designed | unchanged |
| A | Ids must not be reusable across a restart | §4.1 rewritten: boot-nonce epoch as a CAS token; folded into commit 2 |
| B | Do not silently broaden `figaro.queued` | §4.3 rewritten: opt-in `include_carriers`, today's output preserved |
| — | Item 4 proof obligation extended: reload, provider request, the race | §2.5 |

I believe all of these are right and am implementing them as given. If one
turns out to be wrong when it meets the code, I report it rather than working
around it.
