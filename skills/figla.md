---
name: figla
description: Fire-and-forget reminders for a figaro aria. Use figla instead of polling a background process in a loop, sleeping inside a tool call, or spawning ad-hoc `sleep && …` shells, arm a reminder for when you expect work to finish, cancel it if the work reports first, and get a message carrying live state if it does not. Reminders are scoped per aria.
---

# figla: "Figaro là"

Figaro sings *"Figaro qua, Figaro là"*: here, over there. A reminder is figaro
calling to himself from a little further along in time.

## When to reach for it

**Any time you are waiting on something you did not start synchronously.**
Background builds, subagents, long test runs, a worker aria you dispatched.

**Do not** poll in a loop. **Do not** `sleep 300` inside a tool call. **Do not**
spawn `setsid bash -c 'sleep … && …'`. Those three habits burn turns, hold
context hostage, and leak processes: one agent session left 230 orphaned
daemons and tripped a memory-pressure alert doing exactly that.

## The whole surface

```bash
figla arm --aria <id> --in <dur> --about <text> [--watch <cmd>] [--name <n>]
figla list [--aria <id>] [--json]
figla cancel <name> | --aria <id> --all
figla sweep
```

`--in` takes a Go duration: `90s`, `25m`, `1h30m`.

## The pattern

```bash
# dispatch work, then arm a reminder for when you expect it back
figla arm --aria $MY_ARIA --in 25m --about "FX: footer + exit hang" \
      --watch "git -C /home/gluck/dev/figaro-qua/fx log --oneline -1"

# … the worker reports back in time …
figla cancel --aria $MY_ARIA fx-footer-exit-hang

# … or it does not, and a message arrives carrying the state already gathered
```

Your own aria id is in your form as `<system-reminder name="aria_id">`.

## Why `--watch` matters

A reminder that only says *"check on X"* costs you another round trip to find
out anything. `--watch` runs a command **at fire time** and folds its output
into the message, so what arrives is state, not a prompt. Bounded to 10s and
2KB so a hung watch cannot become its own incident.

Good watches: `git -C <wt> log --oneline -1`, `figaro list -a | grep <id>`,
`tail -3 /tmp/build.log`.

## Rules that came from getting it wrong

1. **Calibrate from observed durations, not optimism.** Arm at roughly 1.5× the
   time you expect. A reminder that fires on merely-slow work is noise, and
   noise gets ignored.
2. **Cancel on report.** Make `figla cancel` the first thing you do when a
   worker answers. An uncancelled reminder is a lie waiting to be told.
3. **Reap when a phase ends**: `figla cancel --aria <id> --all`. Reminders are
   aria-scoped precisely so this is safe.
4. **A fired reminder is gone.** There is nothing to cancel afterwards; arm a
   fresh one if the work is still running.
5. **Repeat by re-arming, not by looping.** If you want nagging, arm the next
   reminder when the previous one fires.

## How it works, in case it misbehaves

No daemon. The state directory **is** the registry:
`$XDG_STATE_HOME/figla/<aria>/<name>.json` (`%LOCALAPPDATA%\figla` on Windows),
overridable with `FIGLA_STATE`.

Two carriers, chosen automatically:

- **systemd**, a transient `--user` timer with `--collect`, when a user bus is
  genuinely reachable. Costs nothing while pending and self-collects.
- **detach**, a detached child process, everywhere else: Windows, macOS,
  containers, and Linux hosts whose user bus is missing. *This is the common
  path, not an exotic fallback*: the machine figla was written on has
  `systemd --user` running with **no reachable bus**, so `systemctl --user`
  fails outright.

`list` sweeps as it reads: if the carrier is gone, the record goes with it, so
the registry cannot claim something is pending when it is not.

`figaro` is resolved to an absolute path when the reminder is **armed**, because
a detached child inherits a minimal environment. Looking it up an hour later is
how a reminder silently fails to remind.
