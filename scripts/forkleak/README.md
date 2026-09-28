# forkleak: the rig built to hunt the fork-tail inquiry

A reported sighting: *"occasionally, after forking, and returning to the
original figaro after it has become idle, the forked inquiry is displayed at
the end of the reply."*

This directory is the apparatus that went looking for it, kept so that the next
person to see it does not start from nothing. It reproduces nothing yet. What
it does is state precisely which explanations have been eliminated and how, so
a later attempt can spend its time somewhere new.

See issue #25 for the report, the evidence from a real store, and the capture
protocol for a live sighting.

## The rig

Every trial runs in its own box. Nothing can reach the developer's daemon, the
real store, or a provider:

- `FIGARO_CONFIG_DIR`, `FIGARO_STATE_DIR`, `FIGARO_RUNTIME_DIR`,
  `FIGARO_CACHE_DIR` all point inside the box, and `FIGARO_NO_BIND=1` keeps the
  shell's attend binding out of it.
- the provider is a scripted local gateway: `gw.py` (prose, and a `FORKLEAK_BIG`
  mode for fat replies), `../fake-gateway-tools.py` (turns with tool calls, so a
  turn spans several LTs), or `gw-slowtool.py` (one long tool call, so a fork
  can be taken while a call is outstanding). No credentials, no network, no
  tokens.
- one tmux server per trial on its own socket, pane height read back rather
  than assumed, scrollback captured rather than the visible pane alone.
- every prompt carries a unique token (`PARENTQ…`, `CHILDQ…`), so a leak is a
  grep and not a judgement.
- each trial stops its own daemon and kills its own tmux server on exit. See
  trap 10 in the tmux-testing skill for why that line exists.

Build the binary the trials drive, stamped, then run one:

```sh
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" \
  -o /var/tmp/forkleak/bin/figaro ./cmd/figaro
bash scripts/forkleak/fuzz1.sh 42        # one trial, knobs derived from the seed
bash scripts/forkleak/batch.sh 50 12 4   # twelve trials, four at a time
```

Exit codes are the verdict: `0` clean, `1` the branch's token appeared in the
parent's view, `2` it appeared in the parent's log, `3` the fixture never
reached the state it exists to test, `4` the hop did not land on the parent (a
harness failure, not a leak).

## The six shapes, and what each one eliminates

Knobs are derived from the seed and printed on the first line of every trial, so
a failure names its own repro.

| script | scenario | knobs |
|---|---|---|
| `fuzz1.sh` | the pager attends the parent, forks it with a prompt from the command line, the branch answers, the reader goes back | turns before the fork; head vs `:1` vs `:2`; the parent takes a turn while away; wait for the branch to idle or not; return by `^O` / `:listen` / `:attend`; three decoy arias in between, which evicts the parked parent and forces the `CloneBelow` retention path instead of the shelf; a fork elsewhere, which moves the lineage epoch and drops the shelf |
| `fuzz2.sh` | the pager NEVER leaves the parent; somebody else forks it | fork while the parent is idle or mid-stream; forked by another shell with `--stay` or by the pager itself; a further parent turn; `G` to follow the tail |
| `fuzz3.sh` | the inline incipit surface instead of the pager | fork before the watched turn, mid-turn, or after it; a second inline turn afterwards |
| `fuzz4.sh` | reclamation: `dormant_after_minutes = 1`, `sweep_interval_seconds = 5`, `ui_window_mb = 1`, 60 KB replies, so the parent's agent is reclaimed and its composed turns are evicted and recomposed on the read that lands in them | fork point; branch turns; wait past dormancy; `figaro normalize`; read back through `show` and through a pager |
| `fuzz5.sh` | tool-heavy turns (six nodes per turn) where a turn id and an LT are nowhere near each other, which is where turn-versus-LT arithmetic would show | fork point including a node coordinate `:2.2`; branch turns; return path; shelf pressure |
| `fuzz6.sh`, `toolfork.sh` | a fork taken while the parent is INSIDE a tool call, with and without a prompt, `--stay` and not | `fuzz6` queues a message at a busy aria first; `toolfork` uses a 25 second tool (`yieldMs` in the call keeps it in the foreground) and then asks the parent a follow-up question |

Roughly 90 trials across these shapes have run clean.

## The two in-process probes

Cheaper and sharper than a terminal, and they say which layer is innocent:

- `internal/store/fork_contamination_test.go`: a real `XwalBackend`, a head fork
  and an interior fork, the child writes and the parent writes on, and each
  log is asserted against the other's records.
- `internal/angelus/fork_contamination_composed_test.go`: the composed layer
  with a real lineage, the process-shared `ComposedCache`, and the `AriaReader`
  that serves a DORMANT aria, reading the parent warm, then after the fork, then
  again after reading the child (a cache that hands one aria's run to another
  shows it under that order).

Both pass, which is why they are kept: they pin the invariant the symptom would
violate below the renderer.

## The two scanners

Read-only. They dial the running angelus and read IR; they write nothing.

```sh
figaro list -j > /tmp/arias.json
go run ./scripts/forkleak/forkscan  /tmp/arias.json   # the child's first question, in the parent's log?
go run ./scripts/forkleak/wedgescan /tmp/arias.json   # arias whose log ENDS in unanswered questions
```

`forkscan` walks every conversation-to-conversation fork pair, takes the child's
first own question and searches the parent from the fork base onward.
`wedgescan` looks for the other shape, an aria that accepts prompts and answers
none: input records above the last output the aria ever produced.

## Traps this rig hit, so the next one does not

- **A gateway that reads `Content-Length` reads nothing.** figaro sends its
  request body chunked, so the first forty trials tagged every reply `NOTAG` and
  no reply could be attributed to the question that produced it. `read_body` in
  `gw.py` handles both framings.
- **`sleep` in a bash tool does not hold the call open.** The tool backgrounds a
  command after ten seconds and returns, so the "fork during tool use" fixture
  was testing nothing until the call carried `yieldMs`.
- **`^O` steps ONE entry.** With decoy arias in between it takes as many presses
  as hops away from the parent, and a trial that lands on a decoy must report a
  harness failure rather than a leak. `fuzz1` checks which aria the status bar
  names before it believes any count.
- **A single-node fixture cannot tell a turn from an LT.** With one prose node
  per turn the two numbers move together and any confusion between them is
  invisible. That is why `fuzz5` exists.
- **An absence inside a pager is not an absence.** Panes are tall (58 rows) so a
  turn is not promoted out of view, and the assertions read the scrollback.
