# Tapes

Fixtures for `internal/cli/tape_replay_test.go` and for `figaro replay`.
Format and provenance: `docs/aria-tape.md`.

| Tape | Origin | State |
|---|---|---|
| `spike.tape` | **minted**, not recorded — `-mint-tape`, from the fixture in `tape_replay_test.go` | **RED**: reproduces the wavering range row (tuneTail has no fixed point) |

A tape here is replayed by CI on every run. Two rules before adding one:

- **Read it first if it is a recording.** A tape carries the aria's prose,
  thinking, tool output, arguments and cwd — a `cat` of the wrong file lands in
  it verbatim. `spike.tape` is synthetic and holds nothing but "message-N
  line-M".
- **Say what it is for in this table**, including whether it is expected to
  pass. A red fixture is a defect someone chose to keep in front of the build,
  and the next person needs to know which.
