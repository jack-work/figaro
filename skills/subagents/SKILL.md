---
name: subagents
description: Spawn parallel figaro sessions as subagents, track their progress, and collect results. Use when the user wants to fan out independent work (research, refactoring, testing) to separate figaro instances running concurrently.
---

# Subagents: Parallel Figaro Sessions

Spawn multiple `figaro` processes in parallel, each with its own aria, prompt, and tool access. Monitor progress via log file growth, collect results when they finish, and optionally revisit any aria later.

## Core Commands

| Mode | Command | Use case |
|------|---------|----------|
| Persistent + raw | `figaro send -r -- "<prompt>"` | Background work you want to revisit |
| Ephemeral + raw | `figaro send -er -- "<prompt>"` | Fire-and-forget one-shots |
| Fire-and-forget | `figaro send -f -j -- "<prompt>"` | Launch and get the aria ID back immediately |
| Reattach | `figaro listen <id>` | Watch a running aria's stream |
| Check status | `figaro status <id>` | See if a turn is active or complete |

`-r` (raw) disables ANSI/TUI formatting, giving clean plaintext suitable for piping and log capture. `-j` emits `{"aria_id":"..."}` on stdout for scripting.

## Pattern

1. **Launch subagents** backgrounded with `&`, capturing output to log files.
2. **Monitor** via process count, log file sizes, or `figaro status <id>`.
3. **Collect results** by reading log files after processes exit.
4. **Resume** any persistent aria with `figaro listen <id>` or send follow-ups with `figaro send --id <id> -- "<prompt>"`.

## Example: Parallel Research

```bash
LOG_DIR="/tmp/figaro-subagents"
mkdir -p "$LOG_DIR"

# Task 1
figaro send -r -- "Research the history of Unix signals. Provide a timeline." \
  > "$LOG_DIR/signals.log" 2>&1 &
PID1=$!

# Task 2
figaro send -r -- "Research the history of Windows IPC mechanisms. Provide a timeline." \
  > "$LOG_DIR/ipc.log" 2>&1 &
PID2=$!

echo "Launched PIDs: $PID1, $PID2"
wait
echo "All done. Results in $LOG_DIR/"
```

## Example: Fire-and-Forget with ID Tracking

```bash
# Launch and capture the aria ID for later
ARIA=$(figaro send -f -j -- "Refactor internal/tool to use interfaces" | jq -r .aria_id)
echo "Launched aria: $ARIA"

# Check on it later
figaro status "$ARIA"

# Reattach when ready
figaro listen "$ARIA"
```

## Example: Ephemeral One-Shots (No Aria Persistence)

```bash
# Quick parallel lookups, results only
for topic in "goroutines" "channels" "select"; do
  figaro send -er -- "Explain Go $topic in 3 sentences" > "/tmp/$topic.txt" 2>&1 &
done
wait
cat /tmp/goroutines.txt /tmp/channels.txt /tmp/select.txt
```

## Monitoring

```bash
# Count running figaro subagents
ps -ef | grep "figaro send" | grep -v grep | wc -l

# Check log file sizes (growing = still working, stable = done)
wc -c "$LOG_DIR"/*.log

# Watch growth
watch -n5 'wc -c /tmp/figaro-subagents/*.log'

# List all arias (global view)
figaro list -g
```

## Notes

- Each subagent gets its own context window and token budget.
- Persistent arias (`-r` without `-e`) remain in the store. Clean up with `figaro kill <id>` when done.
- Ephemeral arias (`-er`) are destroyed on completion (no cleanup needed).
- The angelus daemon manages all concurrent arias. It starts automatically on the first `figaro send`.
- Practical concurrency: depends on your provider's rate limits. 4-8 simultaneous arias is typical before throttling.
- Subagents inherit the default loadout (provider, model, skills). Override per-aria with `figaro set --id <id> system.model <model>` after creation.
- Raw mode (`-r`) streams output as it arrives (not buffered until completion like pi's `-p`). Log files grow in real time.
