I see a lot of tool use blocks liek this, where I see the loading spinner remain after its loaded, and the checkmark after its done, with two consectuive border lines. I want to see the loading spinner erased when the tool has completed.

Also the other problem where the execution pauses without largo rendering it as markdown while we wait for a tool to come in is still present. That feels like the server isn't sending the tool use start quickly enough. That might just be an artifact of go concurrency model. If need be we should emit tool use start message before executing the tool so that we are guaranteed to get the message ahead of the thread executing the result and dont get blocked up by it.

─── ⠋ ▶ bash · head -30 /home/gluck/dev/figaro-qua/main/docs/cli-streaming-polish.md ───

# CLI Streaming Polish — Report & Recommendations

*Notes on smoothing the output rate and giving the response a steady,
character-by-character feel — informed by what other LLM CLIs/SDKs are
doing, applied to our specific CLI in `cmd/figaro/main.go` +
`largo`.*

______________________________________________________________________

## 1. The problem we're trying to solve

Right now the figaro CLI writes provider deltas straight into largo as
they arrive over JSON-RPC. That means:

- **Burstiness.** Anthropic SSE chunks vary wildly: sometimes a single
  byte arrives, sometimes 60+ bytes in one delta. The terminal feels
  uneven — long stalls punctuated by big bursts of text appearing all
  at once.
- **No "typing" feel.** When a chunk contains 30 characters they paint
  in a single render tick, which reads as a paste rather than as the
  model thinking out loud. The pleasant typewriter effect modern LLM
  UIs have is missing.
- **Erase-redraw flicker.** Largo's block boundary detection can rewrite
  a line mid-flow when the buffered raw bytes get replaced by glamour-
  rendered output. The bigger the burst we feed it at once, the more
  visible that erase-replace is.
- **TTFT vs. smoothness tension.** Buffering for smoothness must not
  delay the *first* visible character. The user perceives "is it
  alive?" within ~200 ms; that has to keep working.
  ─── ✓ ▶ bash · head -30 /home/gluck/dev/figaro-qua/main/docs/cli-streaming-polish.md ───
  ───
