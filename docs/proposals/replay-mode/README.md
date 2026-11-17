# Replay mode, explained

Status: written for Gluck, 2026-11-17. Issue #114. Nothing is merged. You
called this the lowest priority of today's work, and this note ends by
proposing that we park it.

## What it is for, in one paragraph

We want to know whether a figaro answers better at a higher reasoning
effort. The honest test is to take a turn that really happened, ask the
same question again at a different effort, and compare the answers. Today
we can only do that with a fork, and a fork knows it is a fork. In two
pilots on 11-15 ($47) every replayed figaro wrote about being a copy
instead of doing the task, so the comparisons measured nothing. Replay mode
re-asks an old turn so that the model cannot tell it is a re-run.

## What gives a fork away today

The pilots found four tells:

1. **The fork notice.** A fork is told "you were born from a fork" on its
   first turn.
2. **The clock.** The datetime reminder shows today's date, so a request
   from three days ago reads as stale ("that's from 11-12, I did nothing").
3. **The identity.** The figaro's id reminder shows the fork's new id, not
   the original's.
4. **The tools.** Run in a sandbox, every tool call fails, and the reply
   ends up being about the failures.

## What replay mode does about each

Replay mode masks those four tells, but only in what the model reads. The
real records stay true.

| Tell | Masked how | Off switch |
|---|---|---|
| Fork notice | not sent | `system.replay.notice=false` |
| Clock | the datetime reminder and study windows are frozen at the original turn's time | `system.replay.clock=false` |
| Identity | the model sees the original figaro's id. `ls`, `kill` and the store keep the real id | `system.replay.identity=false` |
| Tools | nothing runs. A call that matches the original turn (same tool, same arguments) gets the original's recorded answer. Outward calls such as `figaro send` get a quiet "ok", and anything else gets a neutral error | `system.replay.tools=false` |

The replayed question also arrives exactly as it did the first time: the
same sender, the same images, the forms studied at the versions they had
then.

## How you would use it

```
figaro fork <id>:<turn> --replay -S system.thinking_effort=high
```

This forks the figaro at that turn and re-asks the turn's original
question. Run it once per effort level and compare the replies.

The second version of the lane (the "rework") made this a switch on a
figaro rather than a one-shot fork flag. `system.replay = {of, turn}` on
any figaro means that figaro's next turn is a replay of that coordinate.
Only you (the owner) can set it. A figaro cannot set it on itself. While
it is set, the figaro refuses ordinary prompts, deliveries and casts.
`figaro show` and the pager label each replayed turn "[replay of
<id>:<turn>]".

## Where it stands

- **Branch:** `lane/replay-rework`, 5 original commits plus 7 rework
  commits, pushed to keel and not merged.
- **Review H** (adversarial) found two blocking bugs. Both send a replay
  out into the real world, which is the one thing a replay must never do:
  1. **It can start a real succession.** If replaying a turn pushes the
     figaro past its retirement threshold, the daemon mints a real
     successor with real tools and hands it the figaro's roles.
  2. **It can message a real figaro.** If a replayed turn fails, the
     failure notice goes to the original turn's sender, a live figaro.
- Review H also found six smaller bugs:
  - Setting replay mid-turn loses the replay.
  - A daemon restart turns the replay into a blank prompt.
  - Your reasoning-effort method silently overrides the effort a replay
    asks for, which defeats the point.
  - Turning tools on while identity stays masked lets the model write to
    the *original* figaro's board.
  - `send -S system.replay=...` marks a live prompt as a replay.
  - Three guards have no test that would catch their removal.
- **Fix lane H** (4f065334) is fixing all eight. It is running its last two
  test gates now.

## What it cannot promise

On an unboxed figaro, "only the owner sets it" rests on the honour label,
the same as fan-out limits. Another figaro can set replay on a figaro
other than itself. That is fine for an evaluation tool you drive, and not
a security boundary.

## Proposal: let H finish, then park it

H is a few turns from DONE and the fixes are worth keeping. After that:

- Keep the branch on keel. Do not put it in a train: nothing uses it until
  someone runs effort evals, and the effort-method question is not on
  Italy's path.
- When the evals come back onto the plan, rebase the branch, give it one
  more review, and ship it on its own.

That costs nothing more today. Say the word if you would rather cut it now
and drop the branch.
