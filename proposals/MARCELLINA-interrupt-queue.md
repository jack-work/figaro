# MARCELLINA — interrupt / cancel control flow and queue semantics

Design note for `feat/interrupt-queue` (forked from main @ e6f52c9). Nothing
below is implemented yet; this is the read of the code as it stands plus the
proposed verbs, flags, RPCs and keys for items 1–6.

---

## 1. How the queue is represented today

**The queue is the agent inbox** — `internal/figaro/inbox.go`. One per aria,
a mutex+cond FIFO of `event` values (`internal/figaro/agent.go`):

```go
type event struct {
    typ        eventType     // eventUserPrompt | eventSet | eventFork
    text       string        // prompt
    chalkboard *rpc.ChalkboardInput
    setPatch   message.Patch
    fork       func() error; forkDone chan error
}
```

Facts that matter:

- **Events have no identity.** No id, no timestamp. `figaro.queued` returns
  bare strings (`Inbox.SnapshotUserPrompts` drops empty-text prompts, i.e.
  pure chalkboard carriers). There is therefore nothing to address for update
  or delete — item 5 needs ids before it needs anything else.
- **Three takers, all prefix-only:** `TakeReadyUserPrompts`, `TakeReadyForks`,
  `TakeReadySet` each lift the *contiguous* prefix run of their own kind, so
  FIFO order across kinds is never violated. `Recv` pops one event and blocks.
- **Two drain regimes.**
  - *Idle*: `act()` → `Recv()` → `runTurn(evt)`. **One event, one turn.**
  - *Mid-turn*: `prepareProviderRound()` / `appendSteeringPrompts()` →
    `TakeReadyUserPrompts()` → `mergePromptEvents()` → **one message** (texts
    joined by `\n`, chalkboard inputs merged in queue order, later wins) →
    `appendUserPrompt(..., steering=true)`. That is the *steering* semantics
    item 1 wants reused.
- **`Prepend`** restores a batch that failed to persist (all-or-nothing).
- **`IsIdle()`** (queue empty) is what publishes `state: active|idle` and rides
  `turn.done{idle}`.

## 2. Where interrupt enters

`figaro.interrupt` → `Agent.Interrupt()` (`agent.go`): under `a.mu`, if
`turnCancel == nil` it returns; else sets `interrupted = true` and cancels the
turn context. **It does not touch the inbox at all.**

The turn context cancel propagates to: the provider `Send` goroutine, every
speculative tool goroutine (`t.Execute(turnCtx, …)`; bash kills the process
group on cancel), and `collectToolResults`' select. `isInterrupted()` also
gates emits (`emitLive` returns early), tool chunk forwarding, and
`emitEnd` (a tool that finishes after the interrupt reports nothing).

`driveOneRound` then hits one of its `a.isInterrupted()` checks →
`repairTurnTail()` → `endTurn("interrupted")` → `runTurn` returns.

**Then `act()` resumes `Recv()` and pops the queued prompts ONE AT A TIME,
each opening its own turn.** That is the behaviour item 1 replaces.

Client side: Ctrl-C (`inputInterrupt` in `stream.go`) with no selection calls
`in.cancel()` and returns `keyStop` — the ctx-done branch of
`mustPromptFigaro` sends `figaro.interrupt` and then **exits the process**
(130). There is today *no* gesture that interrupts and stays attached; that is
items 4 and 6. `figaro hup` (`cli/hup.go`) is the same RPC from outside, no
options.

## 3. How a turn is closed legally (item 4's hard part — already solved)

"Legal to receive new messages" means, concretely, four things:

1. **No dangling `tool_use`.** Every `tool_invoke` in the tail assistant
   message has a matching `tool_result` in a following input message. Two
   guards: `repairInterruptedTail` (boot + top of `runTurn`, synthesises
   results with `tailRepairNotice`) and `repairTurnTail`
   (`turn_repair.go`, the live path).
2. **The truncated assistant message is CLOSED.** `turnState` accumulates the
   partial (`noteAssistant` / `noteTool`) as the stream folds. On interrupt,
   `repairTurnTail` appends `t.assistant` with `StopReason = StopAborted`
   when `!t.committed`, then appends the synthetic `tool_result` tic
   (`interruptedToolResults` — per-tool: real output if the tool finished,
   else the streamed tail plus `interruptedToolNotice`).
   `assembleToolResults` does the same for the in-flight path.
3. **The live UI unit is committed or abandoned.** `endTurn` → `emitCommit`
   (`ariaSrv.Close`) → `finishTurn` → `ariaSrv.Seal(nil)` + `turn.done`.
   `endTurnDiscarding`/`abandonLive` is the path for a partial that never
   reached the IR; committing one that the IR lacks is what duplicates a
   message on the next turn.
4. **The provider wire cache stays consistent.** A repaired assistant is
   appended with **no** translation-cache entry, so the next `Send` takes the
   cache-miss path, which drops thinking blocks rather than replay unsigned
   ones (see reference/architecture: signatures live only in the wire cache).

So item 4 needs **no new store/IR machinery**: the legal-close is exactly what
`Interrupt()` already produces. What is missing is a *gesture* that does it
without killing the CLI session. The work is in the CLI, plus a test that
proves legality by *sending a new prompt* immediately after an interrupt taken
mid-tool-call and asserting the aria answers it.

---

## 4. Proposed wire surface (JSON-RPC, per-aria socket)

### 4.1 Queue identity

`event` gains `id uint64` (and `at int64`), minted under the inbox lock by a
per-aria monotonic counter. Ids are stable, never reused, and survive
coalescing (the combined message keeps the **first** id it absorbed and lists
the rest).

```go
// QueuedPrompt (extended in place; the wire field stays `prompts`)
type QueuedPrompt struct {
    ID     uint64   `json:"id"`
    Text   string   `json:"text"`
    State  string   `json:"state"`            // "queued" | "committing"
    At     int64    `json:"at,omitempty"`     // accepted-at, unix millis
    Merged []uint64 `json:"merged,omitempty"` // ids folded in by a coalesce
    Chalkboard *ChalkboardInput `json:"chalkboard,omitempty"` // drained payloads only
}
```

`state`:
- `queued` — still in the inbox, deletable.
- `committing` — lifted by the drain loop, being appended to the IR. Not
  deletable; the rejection says so.

### 4.2 `figaro.interrupt` — gains an explicit queue disposition

```jsonc
// request
{"queue": "keep"}    // default when the field is absent — today's behaviour
{"queue": "clear"}

// response
{"ok": true, "cleared": false, "queue": [QueuedPrompt, …]}
```

`queue` in the response is **the queue as of the hangup** — one meaning, always
populated; `cleared` says whether those messages were removed or left in place.
`clear` works when the aria is idle too (clearing a queue must not require a
turn in flight). An empty request from an older client behaves exactly as
today.

### 4.3 Queue CRUD

- **C** — `figaro.qua`. No second create path; a queued message *is* a
  submitted prompt.
- **R** — `figaro.queued` (unchanged method, richer elements; empty-text
  chalkboard carriers are now listed rather than hidden, and the *client*
  decides what to render).
- **U** — `figaro.queue.update` `{"id":N,"text":"…"}` →
  `{"result":{"id":N,"outcome":"updated"|"rejected","reason":"…"}}`
- **D** — `figaro.queue.delete` `{"ids":[…]}` or `{"all":true}` →
  `{"results":[{"id":N,"outcome":"deleted"|"rejected","reason":"…"}, …]}`

**Rejection is a result, not an error.** The RPC succeeds; each id carries its
own outcome. Reasons, closed set: `committing` (lifted by the drain loop this
instant), `committed` (already appended to the IR by the running turn — the
agent keeps the ids it drained for the life of the turn, so this reason is
precise rather than "unknown"), `unknown` (never seen, or long since answered),
`closed` (inbox shut). Deleting an id that is `committing`/`committed` is the
"requesting deletion of an in-flight one" case: legitimate to ask, legitimate
to refuse, and the caller is told which.

Atomicity: every mutation runs in one locked pass over the inbox, so a delete
either wins the race with `TakeReadyUserPrompts` or is told `committing`.

---

## 5. Item by item

### Item 1 — interrupt coalesces the WHOLE queue into one message

`Agent.Interrupt()`, inside the branch that actually cancels a turn (so this is
strictly the interrupt path), calls a new
`Inbox.CoalesceUserPrompts(mergePromptEvents)`: one locked pass that folds
**every** queued `eventUserPrompt` into a single event parked at the position
of the earliest one, keeping its id and recording `Merged`. Non-prompt events
keep their relative order.

Semantics are steering's, unchanged and shared code: texts joined by `\n` in
queue order, chalkboard inputs merged so a later value wins
(`mergePromptEvents` / `mergeChalkboardInput`). The combined message then comes
off `Recv()` as ONE event and opens ONE turn as an inquiry (`steering=false`) —
the drain still makes the steer/inquiry call, exactly as documented.

Normal submits are untouched: `SubmitPrompt` still enqueues one event, and an
idle aria still answers each one as its own turn. Nothing changes for a queue
that is never interrupted.

**One deliberate deviation, flagging it for veto:** coalescing across the whole
queue moves a prompt *past* a `set`/`fork` queued in front of it, which the
prefix-only takers never do. For the gesture in question (a human hits Ctrl-C
with three typed messages waiting) there is no interleaved control event, and
"the WHOLE queue as one message" is the requirement. The conservative
alternative is to coalesce each maximal prompt *run*, leaving a queued `set` as
a barrier — say the word and it is a two-line change.

### Item 2 — hangup that CLEARS the queue

`figaro cut [<id>] [-j|--json]` → `figaro.interrupt {"queue":"clear"}`.

Plain output: `cut <id> — interrupted, N queued messages drained`.
`-j`: one line, `{"aria":"…","interrupted":true,"cleared":true,"queue":[…]}` —
the drained messages verbatim (text + chalkboard input), so
`figaro cut -j > lost.json` is a lossless save rather than a lament.

(*Naming:* `cut` = cut the line, against `hup` = hang it up politely. If it
reads wrong beside `fork`/`kill`, the fallback is `figaro hup --clear`, which
costs the two-verb clarity you asked for.)

### Item 3 — hangup that does NOT drain, in CLI and TUI

- **CLI:** `figaro hup [<id>] [-j|--json]` → `figaro.interrupt {"queue":"keep"}`
  (the default). Behaviour is today's; `-j` prints
  `{"aria":"…","interrupted":true,"cleared":false,"queue":[…]}` — the messages
  that survived and will be answered as one combined message per item 1.
- **TUI:** the transcript key below, which is the keep variant.

Two verbs, two help texts, no negated boolean anywhere.

### Item 4 — transcript hotkey that stops the conversation

New keymap row (`internal/cli/keymap.go`), `modeTranscript | modePanel`,
`staysInline` (it addresses a turn that is streaming in a view you are already
in; there is nothing to open):

```
{chord: byteChord('H'), modes: inTranscript | inPanel,
 open: staysInline, why: "…", help: helpHangUp, input: inputHangUp}
```

`inputHangUp` is an **input-level** row (it owns an RPC, not the viewport):
fires `figaro.interrupt {"queue":"keep"}` on a short-timeout background
context, sets a status notice, and returns `keyHandled` — **the session stays
open**. That is the whole difference from Ctrl-C, which returns `keyStop`.

Legality comes free from §3 (truncated assistant closed with `aborted`,
synthetic tool_results for anything in flight, live unit committed, `turn.done`
with `idle` reflecting the retained queue). Proof obligation, as a test:
interrupt mid-tool-call, then `Qua` a fresh prompt on the same aria and assert
it is answered and the IR has no dangling `tool_use`.

### Item 5 — CRUD on the queue

Server: §4.3. Client: a `figaro queue` verb group.

| Command | Effect |
|---|---|
| `figaro queue [<id>] [-j]` | List queued messages: id, state, age, text. |
| `figaro queue rm <id>… [-j]` | Request deletion. Prints one line per id with its outcome. |
| `figaro queue rm --all [-j]` | Same, whole queue. |
| `figaro queue edit <id> -- <text>` | Replace a queued message's text. |

Exit codes: 0 when every requested id was deleted/updated, 1 when any was
rejected (the rejection is *printed*, never swallowed), 2 for argv errors.
The transcript's `Q` panel grows an id column so the ids are visible where the
queue is.

### Item 6 — UI: hang up and stay listening

Same key as item 4 — the affordance IS the gesture, and it is neither a kill
nor a detach:

- help row: `H — hang up: stop the turn, keep listening`;
- status row token `hung up · listening` (a `turnStatus`, via `setNotice` /
  `setTurn`, painted like the existing interrupt notice — never written
  straight to the terminal while the pager owns the screen);
- the pager already has listen semantics past `turn.done`, so no session
  plumbing changes: `running` goes false, `doneCh` is not signalled while
  `inTranscript()`.

`H` is free in every mode table today ('h' is not bound either; uppercase keeps
it away from a future vi-style `h`).

---

## 6. Commit plan

1. `rpc`: queue wire types, interrupt disposition, round-trip tests.
2. `figaro/inbox`: message ids + timestamps, richer snapshot, in-flight ids.
3. `figaro`: **interrupt coalesces the queue** (item 1) + tests.
4. `figaro`: interrupt `queue:"clear"` drains and returns (item 2, server).
5. `figaro`: `figaro.queue.update` / `.delete` with rejection outcomes (item 5, server).
6. `cli`: `hup -j` (keep) and `cut` (clear) (items 2, 3).
7. `cli`: `figaro queue` group (item 5, client).
8. `cli`: `H` hang-up-and-stay-listening key, help row, status token (items 4, 6),
   verified in a real pty per the tmux-testing skill.
9. docs: `skills/figaro/cli.md`, `skills/figaro/reference/ui-stream.md`
   (queue + interrupt disposition), keymap help panel.
10. reconcile with `main` (item 7).

Green gate on every commit: `go build ./... && go vet ./... && go test ./...`.
