# Inbox / Stream Refactor

## Motivation

The figaro `Inbox` currently knows too much about event payloads. `routeToStreams`
(`internal/figaro/inbox.go:44`) switches on `ev.typ`, unpacks `figMsg` /
`translatorPayload`, and rewraps them into `store.Entry[any]`. The inbox should
not own that knowledge — each stream should decide for itself whether a given
event is appendable. We also have two constructors (`NewInbox`, `NewInbox2`) and
a typed `Config` legacy that need to collapse into one shape.

The two consumer streams have incompatible element types
(`Stream[message.Message]` and `Stream[[]json.RawMessage]`), which is the source
of the type error flagged at `agent.go:205`. The fix is to route raw `event`
values through a small decoupling interface, not to homogenize the streams'
element types.

## Target shape

### Constructor

Single constructor:

```
NewInbox(ctx, consumers ...EventConsumer) *Inbox
```

- `EventConsumer` is a new interface (lives in `store`) with one method:
  `TryAppend(ev EventLike) (bool, error)`.
- `bool` = "I accepted this event"; `error` = "I tried but failed."
- Drops the `NewInbox2` name and the typed `Config`.
- Drops `routeToStreams`' switch entirely — the inbox just iterates consumers
  and calls `TryAppend`.

### Call site (`agent.go:206` and the recovery path at `:449`)

```
a.inbox = NewInbox(ctx, a.figStream, a.translator)
```

Each typed stream (`Stream[message.Message]`, `Stream[[]json.RawMessage]`)
implements `TryAppend` itself, so no `Stream[any]` wrapping or generics
gymnastics at the call site.

### Stream interface

Add `TryAppend(ev EventLike) (bool, error)` to `store.Stream[T]` (or a sibling
`EventConsumer` interface that `Stream[T]` embeds). Each typed `Stream[T]`
implementation:

- inspects the event,
- decides whether it owns this event type,
- unpacks the payload internally,
- forwards to its existing `Append(Entry[T], durable bool)`.

`figStream`'s `TryAppend` accepts `eventFigaro` and pulls `figMsg`.
`translator`'s `TryAppend` accepts `eventTranslatorLive` and pulls
`translatorPayload`. Anything else returns `(false, nil)`.

### Event-type leakage

`event` stays in `figaro` — it's a figaro-specific union and moving it to
`store` would invert the dependency. `store` instead defines a tiny
`EventLike` interface (e.g. `Kind() int` plus typed accessors, or just an
opaque `any` plus type assertions inside each stream). Concrete approach:
**`store.EventLike` is an empty interface plus a type-assertion contract** —
streams in `figaro` package type-assert back to `figaro.event` to read the
payload. The store package stays event-agnostic; only figaro-package stream
adapters know the concrete shape.

If the type-assert-on-concrete-type approach feels too loose, the fallback is
a small accessor interface (`Kind() int`, `Payload() any`) on `event` —
decide during step 2.

## Iteration discipline (REQUIRED)

Every step below must:

1. End with a commit whose message names the step (e.g. `refactor(inbox): step
   2 — TryAppend on figStream`).
2. Pass `go test ./...` cleanly **before** the commit.
3. Smoke-test end-to-end: `nix profile upgrade figaro && figaro rest && q
   "hello"`. Figaro must be restarted (`figaro rest`) to pick up the new
   binary; otherwise the running daemon still serves the old code.

No step lands without all three.

## Steps

1. **Introduce `EventLike` + `TryAppend` on streams (no behavior change).**
   Add the interface in `store/stream.go`. Implement `TryAppend` on
   `MemStream` / `FileStream` as a no-op that returns `(false, nil)`.
   Inbox still uses the old switch. Tests: existing suite still passes.

2. **Move the figaro-event switch into figaro-side stream adapters.**
   In `internal/figaro/`, give `figStream` and `translator` real `TryAppend`
   implementations that recognize their respective event types and call
   `Append` internally. Inbox still calls the old switch in parallel — verify
   both paths agree under tests.

3. **Cut over `routeToStreams` to call `TryAppend`.** Replace the switch with
   `for _, c := range consumers { c.TryAppend(ev) }`. Delete the old
   payload-unpacking code from inbox.go.

4. **Collapse `NewInbox` / `NewInbox2` into one constructor.** Rename
   `NewInbox2` to `NewInbox`, delete the typed-`Config` shape, update the two
   call sites in `agent.go` (`:206`, `:449`) and all `inbox_test.go` callers.

5. **Cleanup pass.** Remove now-dead fields on `event` if any consolidated
   away, tighten doc comments on `Inbox` and `Stream`, remove the two
   `agent:` TODO comments at `inbox.go:28` and `:47` and the one at
   `agent.go:205`.

## Out of scope

- Touching the rest of `event`'s union (user prompts, tool results, etc.) —
  those don't route to streams today and shouldn't start now.
- Reworking `Append` / `Condense` semantics on `Stream`.
- Persistence or fingerprint changes.
