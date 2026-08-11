# Tapes

Fixtures for `tape_replay_test.go` and `figaro replay`. Format:
`skills/figaro/reference/tapes.md`.

| Tape | Origin | Expected |
|---|---|---|
| `spike.tape` | minted (`-mint-tape`), synthetic content | PASS since the tailFit fix; it was the repro |
| `stream-eager.tape` | recorded, anthropic direct with `system.eager_tool_streaming = true` | PASS. The arguments STREAM: 17 input patches |
| `stream-buffered.tape` | recorded, same route with the flag off | PASS. The arguments DO NOT stream: 0 input patches, the API having buffered each parameter until it was whole |

Adding one: read it first if it is a recording (tool output lands verbatim),
and say here whether it is expected to fail.

Both `stream-*` tapes were re-recorded when the question stopped riding every
frame: they carry it on 4 frames rather than on all of them, so a replay
exercises the wire the server actually emits. A tape is a RECORDING, and one of
a shape we no longer produce is a fixture that tests nothing.

The two `stream-*` tapes are a matched pair: the same prompt, the same model
and the same route, differing only in whether the provider was asked to stream
tool arguments. They are the cheap way to exercise both halves of the tool
block, a live moving window and a value that arrives whole: without a
provider or a token. `figaro replay testdata/tapes/stream-eager.tape` shows the
first; `--speed 8` is enough to watch it.
