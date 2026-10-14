# Subagents: parallel figaro sessions

Spawn several `figaro` processes in parallel, each with its own aria, prompt
and tool access. Monitor progress by log growth, collect results when they
finish, revisit any aria later.

## The one thing that is not guessable

**`new` mints; `send` speaks to someone who already exists.** An agent's own
shell-outs carry `FIGARO_ARIA=<your id>`, so a bare `figaro send -f -- "..."`
is addressed to *yourself*: the prompt you meant to dispatch arrives back in
your own next turn, and no worker was ever born. Use `new` to spawn, and
`send --id <id>` to steer.

```sh
figaro new -f -j -- "<prompt>"     spawn a worker, print {"aria_id":...}, return now
figaro send -f --id <id> -- "<p>"  steer that worker later
```

## Spawn or fork

`new` starts an aria with no history, so its prompt is the whole brief. If
the work belongs to a conversation you are already in, **fork it instead**:
the branch inherits everything you know, and its prompt is a nudge, not a
brief.

```sh
figaro fork --stay -f -- go                    a branch that picks up where you are
figaro fork --stay -f -- "take the second option"
```

Do not restate the task, the plan or the files to a fork; it can read them.
How a branch learns it was forked, and the fork coordinates, are in
[reference/trunks.md](reference/trunks.md).

## Core commands

| Mode | Command | Use case |
|------|---------|----------|
| Spawn, fire and forget | `figaro new -f -j -- "<prompt>"` | Launch a worker and get its aria id back immediately |
| Spawn, stream to a log | `figaro new -r -- "<prompt>"` | Background work you want to read as it lands |
| One-off | `figaro new -r -S system.ttl=1h -- "<prompt>"` | A throwaway answer; the aria reaps itself |
| Steer | `figaro send -f --id <id> -- "<p>"` | Give a running worker new instructions |
| Reattach | `figaro listen <id>` | Watch a running aria's stream |
| Check status | `figaro status <id> -j` | `dormant`, `idle` or `active` |

`-r` (raw) disables ANSI/TUI formatting, giving clean plaintext suitable for
piping and log capture. `-j` emits one line of JSON on stdout for scripting.
`-f` returns your shell immediately and leaves the daemon working.

## Pattern

1. **Launch subagents** backgrounded with `&`, capturing output to log files.
2. **Monitor** via process count, log file sizes, or `figaro status <id>`.
3. **Collect results** by reading log files after processes exit.
4. **Resume** any persistent aria with `figaro listen <id>` or send follow-ups with `figaro send --id <id> -- "<prompt>"`.

## Example: parallel research

```bash
LOG_DIR="/tmp/figaro-subagents"
mkdir -p "$LOG_DIR"

# Task 1
figaro new -r -- "Research the history of Unix signals. Provide a timeline." \
  > "$LOG_DIR/signals.log" 2>&1 &
PID1=$!

# Task 2
figaro new -r -- "Research the history of Windows IPC mechanisms. Provide a timeline." \
  > "$LOG_DIR/ipc.log" 2>&1 &
PID2=$!

echo "Launched PIDs: $PID1, $PID2"
wait
echo "All done. Results in $LOG_DIR/"
```

## Example: fire and forget with id tracking

```bash
# Launch and capture the aria ID for later
ARIA=$(figaro new -f -j -- "Refactor internal/tool to use interfaces" | jq -r .aria_id)
echo "Launched aria: $ARIA"

# Check on it later
figaro status "$ARIA"

# Reattach when ready
figaro listen "$ARIA"
```

## Example: one-off fan-out (they reap themselves)

```bash
# Quick parallel lookups, results only
for topic in "goroutines" "channels" "select"; do
  figaro new -r -S system.ttl=1h -- "Explain Go $topic in 3 sentences" > "/tmp/$topic.txt" 2>&1 &
done
wait
cat /tmp/goroutines.txt /tmp/channels.txt /tmp/select.txt
```

## Monitoring

```bash
# Count running figaro subagents
ps -ef | grep -E "figaro (new|send)" | grep -v grep | wc -l

# Check log file sizes (growing = still working, stable = done)
wc -c "$LOG_DIR"/*.log

# Watch growth
watch -n5 'wc -c /tmp/figaro-subagents/*.log'

# List all arias (global view)
figaro list -g
```

## Notes

- Each subagent gets its own context window and token budget.
- Every aria remains in the store until removed. Clean up with `figaro kill <id>`, or state a lifetime at birth (`-S system.ttl=1h`) and the daemon removes it once spent; `figaro doctor ttl` lists what is due.
- The angelus daemon manages all concurrent arias. It starts automatically on the first command that needs it.
- Practical concurrency: depends on your provider's rate limits. 4-8 simultaneous arias is typical before throttling.
- Subagents inherit the default outfit (provider, model, skills). Override per-aria with `figaro set --id <id> system.model <model>` after creation.
- **Always pass `--id` when steering a subagent.** Your own shell-outs carry
  `FIGARO_ARIA=<your id>`: you are statically attended to *yourself*, so a
  bare `figaro send`/`set`/`status` addresses you, not the subagent. The id is
  the only way to reach someone else.
- Raw mode (`-r`) streams output as it arrives (not buffered until completion like pi's `-p`). Log files grow in real time.
