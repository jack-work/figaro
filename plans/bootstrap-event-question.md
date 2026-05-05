# Bootstrap Event vs. First-Message Absorption

## The Question

Today, `bootstrapIfNeeded` (`internal/figaro/bootstrap.go`) emits a state-only
tic into `figStream` containing the `system.*` patches (`prompt`, `model`,
`provider`, `skills`) before any user message arrives. It runs once during
`NewAgent`, gated by an idempotency check (`system.prompt` already present in
the chalkboard snapshot).

Should this stay a separate event, or should the patches piggy-back onto the
first user message instead? The `agent:` comment at `bootstrap.go:72` raises
the question and asks for a decision.

## Option A — Keep Bootstrap Event (Current)

**Pros**
- Clear lifecycle separation: bootstrap is a config concern, not a turn
  concern. Lifecycle ordering (`NewAgent` → bootstrap → drain loop → first
  prompt) is easy to reason about.
- Rehydrate is symmetric: `eventRehydrate` already emits the same shape
  (state-only user tic with `Patches`, no `Content`). Bootstrap fits the same
  model.
- Auditable IR log: every aria's stream begins with a clean state-only tic
  documenting the system snapshot at birth. Useful for debugging and
  replaying.
- Works regardless of whether a prompt is ever sent (e.g. arias spawned for
  side-channel inspection, or REST-only health probes).

**Cons**
- One extra event in the stream per aria.
- Requires the `system.prompt` idempotency guard for restored arias.

## Option B — Fold into First User Message

**Pros**
- One fewer event type / fewer entries in `figStream`.
- The bootstrap state arrives bundled with the work that needs it — no risk
  of a "system patches present but no prompt yet" intermediate state.

**Cons**
- Couples lifecycle to user input. An aria with no first prompt has no
  `system.prompt` in its chalkboard. `Info()`, `tokens.ContextSize`, and any
  derivation that reads `system.*` keys would see an empty snapshot.
- Rehydrate becomes asymmetric: rehydrate stays a state-only tic, but
  bootstrap doesn't. The two paths now differ structurally.
- The chalkboard `Apply` and `Save` currently happen at bootstrap time. Defer
  them to first prompt and you lose the on-disk snapshot for fresh arias
  until someone speaks.
- The state-only tic vanishes from the IR log. Replays lose the "this is
  what the aria was born with" record.
- `Message.Patches` on a content-bearing tic already works, but every
  consumer (translator projection, condense, derivations) must continue to
  treat patches-with-content the same as patches-without — easy to break.

## Recommendation — Option A (keep the bootstrap event)

The "extra event" cost is negligible (one tic per aria lifetime). The
symmetry with `eventRehydrate` is structurally valuable: both are
"timeline-visible state mutations that produce zero wire output", which is
exactly the contract `Message.Patches` was designed for (see
`internal/message/message.go:99-109`). Folding bootstrap into the first
prompt would create a special case where the comment in `message.go` ("State-
only tics (bootstrap, rehydrate) carry only Patches") becomes a half-truth.

The lifecycle decoupling also matters for any future aria that doesn't begin
with a user prompt — REST-spawned, scheduled, or programmatically constructed
arias all benefit from being fully bootstrapped at `NewAgent` time.

**Open questions** — none material. The idempotency guard is one line and
already correct.

## What To Do (Option A)

Effectively nothing in code. Just delete the `agent:` comment block at
`bootstrap.go:72-74` to close the question, and optionally tighten the
function comment at `bootstrap.go:15-17` to note the rehydrate symmetry as
the rationale.

## (Reserved) Migration Steps If Option B Were Chosen

Listed only for completeness; **not** the recommendation.

1. Move patch construction out of `bootstrapIfNeeded` into a helper
   `buildBootstrapPatch(model)`. Call it from `NewAgent` to stamp the
   chalkboard immediately (so `Info()` and derivations work pre-prompt), but
   do **not** append a tic.
2. In the `eventUserPrompt` handler in `agent.go`, on the first prompt of a
   fresh aria (detected via empty `figStream.Durable()`), prepend the
   bootstrap patch to `inProgressTic.Patches` before `finalizeAndSend`.
3. Verify rehydrate still works on a restored aria — restored arias must
   skip step 2 (chalkboard already has `system.prompt`).
4. Update the `Message.Patches` doc comment in `message.go` to reflect that
   bootstrap no longer emits a standalone state-only tic.

### Iteration Discipline (REQUIRED if Option B is pursued)

Every iteration must:
- (a) commit with a clear message describing the step,
- (b) run `go test ./...` and ensure all tests pass before commit,
- (c) end-to-end smoke-test via `nix profile upgrade figaro && figaro rest && q "hello"`,
- (d) **verify rehydrate still works on a restored aria** — kill `figaro rest`, restart, and confirm an existing aria still has its `system.*` keys and produces a valid first turn.
