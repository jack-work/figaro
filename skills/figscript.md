---
name: figscript
description: Strategies for writing shell scripts that invoke the figaro agent as a subprocess, ideally fanning work out in parallel. Use when scripting LLM-driven steps with figaro.
---

# Figscript

Scripting *with* figaro means treating the CLI as a worker pool. The
agent is the slow part; everything else is plumbing.

## Read the help first

The CLI surface drifts. Before scripting, run:

```bash
figaro --help
figaro send --help
figaro new --help
figaro aria --help
```

Pin behavior to what `--help` says today, not what this skill remembers.

## The two scripting workhorses

- `figaro send -er -- <prompt>`: ephemeral + raw. Use for one-shot
  transforms (classify this line, summarize this blob). `-e` is
  `--ephemeral` (no aria persisted, killed after the turn). `-r` is
  `--raw` (no ANSI, no markdown; verbatim stdout). The two are
  orthogonal: you can have `-e` without `-r` (ephemeral but pretty)
  or `-r` without `-e` (persistent named aria, raw stream).
- `figaro new -- <prompt>`: opens a fresh aria with a chosen
  loadout / patch. Use when you want the work auditable later, or when
  the sub-task needs tools / a multi-turn life of its own.

`figaro send` also has `-x` / `--exec` to wrap the prompt in a
"emit bash only" system note and pipe the reply through `bash -c`.
`-ex` is ephemeral exec; `-ex -y` skips the confirmation; `-n`
prints the script instead of running it. (`-r` is silently ignored
with `-x` since the script governs its own output.)

## Parallel fan-out

Independent prompts → run them concurrently. The angelus handles
multiple arias fine.

```bash
# xargs -P: N workers, one prompt each
printf '%s\n' "${tasks[@]}" \
  | xargs -I{} -P 8 figaro send -er -- "Classify: {}"

# GNU parallel with structured args
parallel -j 8 figaro send -er -- "Summarize {}" ::: "${files[@]}"

# Plain background jobs + wait
for t in "${tasks[@]}"; do
  figaro send -er -- "$t" > "out/$t.txt" &
done
wait
```

## Discipline

- **One responsibility per invocation.** Easier to retry, cheaper to
  rerun. Don't pack five sub-tasks into one prompt if you can fan out.
- **Capture stdout, check exit codes.** Treat figaro like any other
  unix tool that can fail.
- **Quote prompts carefully.** Use `--` to separate flags from prompt
  text; single-quote when interpolating user data into a shell prompt.
- **Stream, don't buffer.** `-r` keeps output flowing unstyled: useful when
  piping into a downstream consumer that reacts incrementally.
- **Don't recurse blindly.** A figaro process spawning figaro
  processes is fine; a figaro *agent* spawning itself can wedge if
  the same aria is reentered. Use `new` / `send -er` for sub-work, not
  `aria <self-id>`.

## When to reach for an aria instead

If the sub-task needs many turns, tool use, or human follow-up
later, `figaro new --` is right. If it's a stateless function call,
`figaro send -er --` is right. When unsure, start ephemeral.

## Polling job state

When you fan out with `figaro new` (or `send` without `-e`), each
aria carries a **`state`** field the daemon publishes: the way to
tell whether a worker is still going. Three values:

| state | meaning |
|---|---|
| `dormant` | not loaded in memory; nothing running |
| `idle` | loaded, inbox empty (no turn in flight) |
| `active` | inbox non-empty: currently working a turn |

(Ephemeral `send -er` calls are synchronous: they block until done, so
polling is unnecessary. State polling matters for `new`-minted arias
and for `send` invocations you've backgrounded.)

**One-shot poll across the forest:**

```bash
figaro list -j | jq -r '.[] | select(.kind=="conversation") | "\(.state)\t\(.id)\t\(.mantra)"'
```

**Poll one aria:**

```bash
figaro status <id> -j | jq -r .state
# or: figaro list -j | jq -r '.[] | select(.id=="<id>") | .state'
```

`figaro status <id>` (non-JSON) also shows it near the top.

**"Is anything still working?": exit-code style:**

```bash
figaro list -j | jq -e 'any(.state == "active")' >/dev/null && echo busy || echo quiet
```

**Wait-for-idle loop** (barrier after a fan-out of named workers):

```bash
ids=(a1b2c3d4 e5f6a7b8 ...)
while :; do
  busy=$(figaro list -j | jq --argjson ids "$(printf '%s\n' "${ids[@]}" | jq -R . | jq -s .)" \
    '[.[] | select(.id as $i | $ids | index($i)) | select(.state=="active")] | length')
  [ "$busy" -eq 0 ] && break
  sleep 2
done
```

**Live tail one aria** (pushed, not polled):

```bash
figaro listen <id>
```

Same live-render stream `send` uses mid-turn: tool calls and text
as they happen. Ctrl-D detaches without killing the turn.

**Caveats:**

- `active` is edge-triggered off inbox depth. A turn parked waiting on
  the provider still shows `active`; the flip to `idle` happens when
  the drain loop finishes the event. Good for "is work in flight?" -
  not a heartbeat.
- `last_active` (ms epoch, in `list -j` / `status -j`) is your recency
  signal for dormant/idle arias: pair it with `state` if you want
  "working *and* recently touched".
- No push notification of state transitions on the CLI surface. Poll
  `list -j` on an interval, or `listen` for the frame-level truth.
- Clean up named workers when done: `figaro kill <id>` (add
  `--recursive` for a whole subtree) so they don't accumulate in
  `figaro list`.
