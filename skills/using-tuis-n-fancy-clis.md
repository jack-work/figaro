---
name: using-tuis-n-fancy-clis
description: Driving TUIs and interactive/"fancy" CLIs through a tmux PTY: input, synchronizing on render, capturing scrollback, and fuzzing for visual glitches. Plain CLIs are fine through bash; reach for tmux when UX reproducibility matters, when you need to interact with the same persistent terminal content across steps, or when a program takes over the terminal and its escapes would bleed into the parent.
---

# Using TUIs & fancy CLIs

Plain, non-interactive CLIs run fine straight through bash. Reach for
tmux when:

- **UX reproducibility is a concern**: you want a real PTY of a known
  size, drivable and inspectable deterministically.
- **You need to interact with the same terminal content across steps**
, a persistent session you send keys to and re-read, rather than
  one-shot command invocations.
- **The program takes over the terminal**: bubbletea, huh, fzf, vim,
  htop, less in interactive mode: whose escape sequences (alt-screen,
  cursor visibility, line wrap, mouse modes) would otherwise bleed into
  the parent terminal. Especially the parent agent's terminal.

Wrap it in a tmux session.

## The minimum dance

```bash
SESS="smoke-$$"
tmux new-session -d -s "$SESS" 'env … your-tui-command here'

# Drive input. Real key names: Enter, Escape, BSpace, C-c, C-n, etc.
tmux send-keys -t "$SESS" 'passphrase' Enter
tmux send-keys -t "$SESS" 'j' Enter

# Inspect rendered state.
tmux capture-pane -t "$SESS" -p

# Clean up. EXIT trap so this runs even on script abort.
tmux kill-session -t "$SESS"
```

## On killing

`tmux kill-session` is always safe: terminal state lives inside
tmux's virtual pty and dies with it. Even if the inner TUI never
got a chance to emit its cleanup escapes, the parent terminal is
untouched.

If you bypass tmux and SIGKILL a bubbletea program directly,
`printf '\033[?1049l\033[?25h\033[?7h\033c'` (or `tput reset`)
restores the terminal.

## Synchronizing on render

`send-keys` returns immediately; the TUI hasn't repainted yet. Don't
`sleep` a fixed guess: poll until the pane stops changing:

```bash
wait_idle() { # return once the pane is stable for ~6s (no streaming)
  local prev="" s=0 i
  for i in $(seq 1 90); do
    sleep 2
    local c; c=$(tmux capture-pane -t "$SESS" -p)
    [ "$c" = "$prev" ] && s=$((s+1)) || s=0; prev="$c"
    [ "$s" -ge 3 ] && return 0
  done
  return 1   # timed out (hung? waiting on a prompt?)
}
```

## Capture with history

`capture-pane -p` gives only the visible pane. To inspect content that
already **scrolled off the top** (where painter desyncs hide), add
`-S -` for the full scrollback:

```bash
tmux capture-pane -t "$SESS" -p -S - > cap.txt
```

## Fuzzing a CLI/TUI for visual glitches

Long-running TUIs (streaming agents, progress UIs, anything that
repaints a live region while preserving scrollback) accumulate
rendering bugs that only show up past a length/size threshold:
duplicated rows, frozen spinners, cursor desync, bookend occlusion.
Hunt them deterministically:

1. **Build the CURRENT binary to /tmp** and run THAT. A stale installed
   binary just replays already-fixed bugs.
2. **Use a deliberately SHORT pane** (`tmux new-session -d -s S -x 100
   -y 24`). Forcing the output to scroll is the whole point: the
   scroll boundary is where relative-cursor painters desync.
3. **Isolate state** so the run is non-impactful and repeatable (point
   the tool's runtime/state dirs at `/tmp`, keep auth/config inherited,
   `cd` into a scratch dir). Drive only read-only / idle work: `ls`,
   `cat` of system files, questions: nothing that mutates real files.
4. **Drive the stressors**, `wait_idle` + capture after each:
   - long single turns (output well past the viewport),
   - **parallel** subtasks whose combined output overflows the pane,
   - reflowing markdown (tables, lists, fenced code),
   - unicode/emoji/CJK and box-drawing,
   - **resize mid-stream**: `tmux resize-window -t S -y 16` while it
     streams (exercises SIGWINCH re-render),
   - rapid key toggles near the boundary: `tmux send-keys -t S C-o`.
5. **Detect** on the `-S -` capture (strip ANSI first): leftover
   spinner-frame glyphs in a *settled* frame, consecutive duplicate
   non-blank rows, rows wider than the pane (would wrap → desync).
6. **Reproduce in a unit/VT harness** before fixing, a real-terminal
   capture confirms it's real, a deterministic harness gives you a
   regression test and isolates it from capture artifacts.
7. **Clean up**: `tmux kill-session`, kill any isolated daemon you
   spawned, `rm -rf` the temp dirs. Use an `EXIT` trap so abort cleans
   up too.

**Width-detector caveat.** A naïve display-width counter (e.g. Python
`unicodedata.east_asian_width`) miscounts ZWJ sequences and flag emoji
(`👨‍👩‍👧`, `🇯🇵`): it sums the components and screams "over-wide" on a
line that renders fine. The tool's OWN width library is authoritative
(for Go, `mattn/go-runewidth`'s `StringWidth`). Cross-check any
over-wide flag against it before believing it.

**A real limit to know:** a scrollback-preserving painter cannot
rewrite a row once it scrolls into history. Content that's still
*mutating* when it scrolls off (e.g. a tool flushed mid-run) keeps
whatever it showed at flush time: the fix is to commit a *stable*
representation at flush, not to chase the impossible after-the-fact
update.

## Still to flesh out

Send-keys recipes for arrow keys, function keys, modifiers, paste mode.
