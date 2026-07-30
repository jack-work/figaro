# Driving figaro from an agent or a script

What is different when the caller is not a human at a terminal. The verbs
themselves are in [cli.md](cli.md); this file is the behaviour around them.

## You already have an identity

Every bash call an aria makes carries two variables:

```
FIGARO_ARIA=<the aria's own id>    identity
FIGARO_NO_BIND=1                  may not mutate the pid binding
```

So a shell-out is statically attended to the aria that spawned it. These need
no `--id`, because they already mean you:

```sh
figaro state                 your chalkboard
figaro set mantra "..."      patch your chalkboard
figaro status                your provider, model, context
figaro show -n 5             your last 5 turns
figaro send -f -- <text>     a note to yourself; mid-turn it steers the turn
```

It is an identity, not a binding. Nothing is written to the angelus, nothing is
inherited from the terminal that started the daemon, and it cannot be moved.
`figaro attend` refuses from inside an aria for exactly that reason. Reaching
anyone else takes an explicit id on every verb:

```sh
figaro send --id <other> -- <prompt>
figaro show --id <other>
figaro kill <other>
```

A dangling id, meaning your aria was killed while your shell-out was running,
reports "nothing bound" rather than erroring, so a script keeps its normal
branch.

## Binding is off by default for you

The pid binding exists so an interactive shell can `attend` once. Scripts must
not inherit it. Binding is disabled when `FIGARO_NO_BIND=1` is set, when
`--no-bind` is passed, or when neither stdin nor stderr is a TTY. A TTY on
either one is enough to opt in, which covers `figaro send ... | jq` typed by a
human.

The failure this prevents is real: a child figaro grabbing its parent's binding
and forking the wrong aria.

## Sub-arias

```sh
figaro send -er -- <prompt>        one shot, ephemeral, raw. Nothing to clean up.
figaro new -- <prompt>             a persistent sub-aria you can keep talking to
figaro send --id <id> -r -- <p>    talk to it again
figaro kill <id>                   when done, so it stops showing up in ls
```

`-e` (ephemeral) and `-r` (raw) are orthogonal: one is about persistence, the
other about formatting. Fan several out in parallel when the sub-questions are
independent. The **figscript** skill has the full recipe, and **subagents** has
the fan-out patterns; neither is repeated here.

## Knowing whether a sub-aria is still working

Every aria publishes a `state`:

| state | meaning |
|---|---|
| `dormant` | not loaded in memory, nothing running |
| `idle` | loaded, no turn in flight and nothing queued |
| `active` | a turn is in flight, **or** work is queued behind one |

The source, in `Agent.Info` (`internal/figaro/agent.go`):

```go
state := "idle"; if a.turnCtx != nil || !a.inbox.IsIdle() { state = "active" }
```

`dormant` is stamped by the angelus when it merges disk-backed arias into the
list response.

**The `turnCtx` half is the trap.** A running turn normally has an empty inbox,
because the event was dequeued to start it. Read the predicate as inbox-only
and a busy agent reports `idle`, so a supervisor polling "is my worker still
going" collects it mid-flight. This file said the wrong half once. Trust the
quoted line over any prose about it, including this paragraph.

Recipes:

```sh
figaro status <id> -j | jq -r .state                          one aria
figaro list -j | jq -e 'any(.state == "active")' >/dev/null   busy, by exit code
figaro listen <id>                                            live frames, pushed
```

`figaro list -j` is the global view: every aria, including anchors. It is the
right tool for a supervisor polling a fleet it spawned, and the wrong tool for
"who is next to me", which is scoped `figaro ls`.

Caveats worth knowing before you build on this:

- `active` is edge-triggered off inbox depth. A turn parked waiting on the
  provider still reads `active`; the flip happens when the drain loop finishes
  the event.
- `last_active` (ms epoch, in `list -j` and `status -j`) is the recency signal
  for dormant and idle arias.
- There is no push notification of state transitions on the CLI surface.

## Do not poll in a loop

Polling in a loop burns turns and holds context. When you are waiting on
something you did not start synchronously, arm a reminder instead and let it
call you back. That is what the **figla** skill is for. Never sleep inside a
tool call, and never spawn an ad-hoc `sleep && ...` shell.

## Backgrounding, and the pipe that decides it

The bash tool runs each command in its own process group and waits up to
`yieldMs`. If the command is still running it is backgrounded as a tracked
session rather than killed, and you follow it with the `process` tool.

Completion is signalled by the output pipe reaching EOF, not by the foreground
exiting. Two consequences:

- A bare `cmd &` child inherits the pipe, so the call stays open until that
  child finishes. Its work is captured but is **not done when the call
  returns**.
- A redirected `cmd >/dev/null 2>&1 &` releases the pipe, so the call returns
  at once and the child orphans: still running, untracked, invisible to the
  `process` tool.

Use `background: true` with the `process` tool, or `cmd & wait`, or serial
commands. Do not background with a bare `&` and assume completion.
