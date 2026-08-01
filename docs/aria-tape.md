# Aria tapes: recording the wire, replaying the bug

A **tape** is a recording of one CLI's conversation with one agent: every
JSON-RPC message that crossed the socket, in order, with the time it crossed.
Replaying a tape reproduces what the terminal was told, exactly and on
schedule — through the real renderer, with **no daemon, no agent, no provider,
no aria store and no tokens**.

It exists because of a specific failure. On 2026-08-01 the transcript's range
row wavered between `1043–1072/1072+` and `546–575/575+` for a whole turn.
Diagnosing it took a worktree, a scratch tree, five hand-written fixtures and
two hours, and the fixture that finally reproduced it was one nobody would
have written on purpose. A tape of that turn would have been the whole
investigation.

```sh
figaro listen <id> --record bug.tape     # free: listening costs no tokens
figaro replay bug.tape                   # watch it again, exactly
figaro replay bug.tape --summary         # what is on this tape?
go test ./internal/cli -run TestTape -tape bug.tape   # assert on it forever
```

## What is on a tape

NDJSON, because the wire already is (`jkrpc`: "JSON-RPC 2.0 over NDJSON").
Line 1 is the header; every other line is one frame.

```json
{"tape":1,"aria":"9f5bc9fa","started":"2026-08-01T17:11:52.809826435-04:00",
 "cols":80,"rows":24,"term":"tmux-256color","binary":"<sha>",
 "command":"figaro listen 9f5bc9fa --record bug.tape","note":"what I was hunting"}
{"t":0.000128,"dir":"out","msg":{"jsonrpc":"2.0","id":1,"method":"figaro.read","params":{…}}}
{"t":0.001843,"dir":"in","msg":{"jsonrpc":"2.0","id":1,"result":{"parts":[…]}}}
{"t":2.117904,"dir":"in","msg":{"jsonrpc":"2.0","method":"figaro.aria","params":{"parts":[…]}}}
```

Three decisions worth their ink:

- **`t` is seconds since the header's `started`, not a wall clock.** A tape
  must replay at a different hour and at a different speed; only a relative
  clock is meaningful under both.
- **`msg` is verbatim.** The recorder splits the stream on message boundaries
  and writes the bytes through. Re-encoding would normalize key order and
  number formatting, and a replay would then be reproducing our marshaller
  instead of the server.
- **Both directions.** The `in` half is what the renderer consumed; the `out`
  half is there because a replay has to *answer* the client's requests, and
  because the order a pager asks for history in is itself behaviour under test.

## Where the recorder hangs

One seam, one line, and nothing in the daemon:

```go
// internal/transport
type Tap func(net.Conn) net.Conn                    // middleware over a live conn
func DialWith(ep Endpoint, tap Tap) (*jkrpc.Conn, error)

// internal/figaro
func DialClientWith(ep, onNotify, tap) (*Client, error)

// internal/cli
figaro.DialClientWith(ep, onNotify, tapeTap(rec))   // rec == nil ⇒ conn unwrapped
```

Below `jkrpc`, because this is the only place that sees **both directions as
bytes**. A tap higher up (wrapping `NotifyHandler`, say) would see
notifications but not the responses to our own calls, and would see values our
decoder produced rather than the ones the server sent.

With no `--record`, `Tap` returns the connection unchanged — the ordinary path
does not even allocate a wrapper. `TestNilWriterIsNotAWrapper` pins that.

### The server-side option, REJECTED

`figaro.Agent` has a matching seam — `Subscribe(Notifier)`, which `serveConn`
uses to hand each connection's jkrpc server the frame stream. A decorator there
would record every client of every aria from inside the daemon.

**The owner has ruled that out, and the ruling is the design.** A daemon that
can record is a daemon that records arias nobody asked about: the flag would
live far from the conversation it captures, one careless default would tape
every aria on the machine, and the files would accumulate where no one is
looking. Recording is a property of a REQUEST, not of the server.

So the tap is client-side and per-invocation. The daemon has no recording code,
no recording flag and no idea it is being recorded. The scope of a tape is
exactly one CLI process's one connection, and it ends when that process does.

## How replay works

`figaro replay` stands up a unix socket in a temp dir, speaks the recorded
frames over it, and then calls **`tailFigaro` — the same function `figaro
listen` calls**. Same renderer, same pager, same frame pacer, same catch-up
read. A harness that fed pages into the renderer by hand would be testing a
second implementation of the client, which is the one thing a repro must not
do.

The tape is used two different ways, because a replayed client is a live
program, not a rewind:

- **Requests are answered by lookup.** The recorded response for the same
  method is replayed, oldest unused first. A client asking a method twice is
  walking a cursor, and the recording walked it in that order. An unmatched
  method returns a null result rather than an error — the pager treats a failed
  read as a desync and retries forever.
- **Notifications are pushed on the clock**, on the recorded schedule, scaled
  by `--speed`. That is the half that reproduces a paint bug, because a paint
  bug is usually about *when* frames arrive as much as what is in them.

`--speed 0` plays as fast as the client will take them: no wall-clock waits,
which is what a test wants and what a human never does.

## Determinism: what had to be pinned

The renderer reads a clock in three places. All three are already injectable —
this was luck, and it is worth keeping true:

| Source | Where | How replay pins it |
|---|---|---|
| session clock in the status row | `newSessionStatus(id, startedAt)` | `tailOpts.startedAt` from the tape header |
| frame-rate gate + trailing flush | `framePacer.now` / `.after` | real time; frame *count* varies, settled frames do not |
| resync interval | `transcript.now` | same |

The spinner is **not** a clock: `sessionStatus.tick` is a counter advanced by
the tick loop, so a given event sequence gives a given glyph.

What is *not* pinned, and what that costs: the frame pacer means a replay at
`--speed 1` may paint a different NUMBER of frames than the recording did.
Every *settled* frame matches; intermediate ones may be coalesced differently.
So golden-frame assertions belong on the headless path (below), which renders
one frame per page and is exactly reproducible —
`TestTapeReplayIsDeterministic` runs the same tape twice and compares.

Terminal geometry rides in the header (`cols`/`rows`) and the headless replay
uses it, so a tape taken at 80×24 is replayed at 80×24.

## The two replay paths, and which oracle each is

| | `figaro replay` (pty) | `go test -tape` (headless) |
|---|---|---|
| Renderer | real, whole CLI over a real socket | real transcript + client, `FakeTerminal` |
| Terminal | real pty (tmux) | none |
| Sees | escapes, scrollback, resize, what a human sees | rows, indices, invariants |
| Cost | seconds of wall clock | milliseconds |
| Use | "show me the bug" | "never again" |

Both are needed. The pty path is the only honest oracle for anything that
paints (see `reference/ui-testing.md`); the headless path is the one CI can
run on every commit.

## The regression gate

`internal/cli/tape_replay_test.go` replays every tape in
`internal/cli/testdata/tapes/` (or one named with `-tape`) and asserts:

- **the retained row total may only grow while following.** Following means
  the window is anchored at the newest message and history is only added behind
  it; a total that falls has dropped rows the reader can still scroll to, and a
  total that falls and rises repeatedly is the pager arguing with itself. This
  is exactly the 2026-08-01 defect.
- **two replays of one tape paint identical rule rows** — without which the
  first assertion is worth nothing.
- **the tapes themselves are readable and self-describing.**

Measured on this branch:

```
live-9f5bc9fa.tape  (real recording, 113 frames, 1m58s)   PASS
spike.tape          (minted fixture, one 500-row message) FAIL
    the retained window shrank 10 times while following (first at page 3 of 41)
```

The same `spike.tape` under `figaro replay --speed 0.04`, in tmux, at 100×33,
sampled twice a second — the master's symptom, on demand:

```
1120–1149/1149+   1082–1111/1111+   1086–1115/1115+   1200–1229/1229+
1098–1127/1127+   1102–1131/1131+     82–111/111+     1220–1249/1249+
```

## Minting a tape without a recording

`go test ./internal/cli -run TestMintTapeFromFixture -mint-tape out.tape`
writes a tape from a Go fixture. That is the other direction of the same road:
a recording turns a bug someone *saw* into a test; minting turns a bug someone
*reasoned about* into something a human can watch in a terminal — which is how
you find out whether the reasoning was right.

## What a tape holds

**A tape carries the aria's content**: prose, thinking, tool output, tool
arguments, cwd, mantra, chalkboard metrics — everything the pager could paint.
It is a transcript in a different coat.

Consequences, not suggestions:

- Recording is **opt-in per invocation** and writes only where you point it.
  There is no ambient recording and no default path.
- A tape promoted to a **committed fixture** must be read before it is
  committed. Tool output is the hazard: a `cat` of a config file, an `env`, a
  token echoed into a log. The test suite logs a warning over 8 MB, which is a
  size guard and not a secrets guard — there is no substitute for reading it.
- Prefer `listen --record` over `send --record` for fixtures: listening spends
  no tokens, so a fixture costs nothing but disk.

## Status

Prototype, on `test/aria-tape`. Built and measured:

- `internal/tape` — format, writer, reader, the tap (5 tests).
- `transport.Tap` / `DialWith`, `figaro.DialClientWith` — the DI seam.
- `figaro listen --record`, `figaro send --record`, `figaro replay`.
- `internal/cli/tape_replay_test.go` — the headless gate and the minter.

Not built, in rough order of value:

1. **A trim tool.** A two-minute tape is 250 KB; a bad turn on a real aria can
   be tens of MB. `figaro replay --from/--to` plus a `--write` would cut a
   fixture down to the frames that matter.
2. **Golden frames on the headless path.** The infrastructure already exists
   (`-update-frames`, `testdata/transcript_frames.golden`); a tape golden is
   the same trick with the pages coming off disk.
3. **Input on the tape.** Keystrokes are not recorded, so a bug that needs a
   scroll or a `Ctrl-T` cannot be replayed hands-free yet. The tape format has
   room: a third direction (`"key"`) with the same clock.
4. **A scrubber**, for promoting recordings of real work into committed
   fixtures.
