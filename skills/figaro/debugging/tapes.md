# Aria tapes

Every JSON-RPC message between one CLI and one agent, with the time it crossed.
Replay drives the real renderer: no daemon, provider, store or tokens.

```sh
figaro listen <id> --record bug.tape    # free; Ctrl-D stops (Ctrl-C interrupts the turn)
figaro replay bug.tape [--speed 4] [--summary]
go test ./internal/cli -run TestTape -tape bug.tape
```

NDJSON: header, then frames. `msg` is verbatim; `t` is seconds since
`started`, so a tape replays at any hour and any speed. `in` is what the
renderer consumed, `out` what a replay must answer.

```json
{"tape":1,"aria":"9f5bc9fa","started":"…","cols":80,"rows":24,"binary":"…"}
{"t":2.117904,"dir":"in","msg":{"jsonrpc":"2.0","method":"figaro.aria","params":{…}}}
```

**Seam**: `transport.Tap func(net.Conn) net.Conn` in `DialWith`, below jkrpc -
the only place seeing both directions as bytes. Nil tap, unwrapped conn.
Nothing in the daemon: recording is a property of a request, one process, one
connection. Replay serves a temp socket and calls `tailFigaro`, the same
function `listen` does; requests answered by lookup, notifications on the clock.

**Determinism**: plain replay runs on the wall clock (pacer, spinner, timers),
so frame *count* varies. `--lockstep <sock>` does not: a driver steps it and
step n is tape time n/fps (`--fps`, default 60). Each step delivers the frames
and fires the timers due by then, waits for the async work they started
(`goSettled`), draws once and writes `OSC 7717;step=n BEL`; the socket
answers `done n bytes` (running pty total, markers included). Animations run
at `--anim-fps` or `rate n` (default 12). The renderer sits on its own pty
sized from the header; keys, resizes and the ambiguous-width probe never reach
it. Every time read on the render path goes through `clk`: a direct
`time.Now` there breaks `TestALockstepReplayIsThePureFunctionOfItsSteps`.

**Isolation**: a replay answers from the tape or refuses. `S`, `:form` and
`:state` read the subject's form from the tape; every other verb says "a replay
has only its tape", and the process dies before it would dial a daemon or
open the vault.

**A tape holds the aria's content**: prose, thinking, tool output, cwd. Read a
recording before committing it as a fixture; `testdata/tapes/README.md` records
which are expected to fail.

Unbuilt: trim, tape goldens, keystrokes as a third direction, a scrubber.
