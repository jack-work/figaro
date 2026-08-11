---
name: inquire-figaro
description: Fan a single question out to every other opened figaro aria in parallel, gather each response as it lands, and synthesize the results (into chat, a document, or a tool action). Use when the user says "ask my other figaros", "poll my arias about X", "gather everyone's take on Y", or otherwise wants a cross-aria roundtable. Assumes the target arias already exist. Always exclude the current aria.
---

# Inquire-figaro

A one-shot "ask everyone else" fan-out. Given a user question and a
time window, dispatch that question to every eligible aria, collect
each reply as it completes, and synthesize.

*This skill is a stopgap.* Once the `figaro` binary grows native async
fork / broadcast primitives, fold this into the `figaro` skill and
delete this file.

## Ground rules

- **Never inquire yourself.** Your aria id arrives in the form
  as `<system-reminder name="aria_id">`. Filter it out.
- **Only already-opened arias.** Don't `figaro new`. The set is
  discovered, not created.
- **Kind must be `conversation`.** Skip loadout/null anchors.
- **Respect the time window.** Filter on `last_active` (ms epoch)
  relative to now. If the user says "last hour", "today", "last 10
  min", convert to a ms cutoff. If they give no window, ask once,
  briefly.
- **In parallel, always.** One background bash session per target.
  Do not serialize.
- **Consume responses as they land.** Poll the sessions; don't wait
  for the slowest to start rendering the synthesis skeleton.

## The recipe

### 1. Resolve the target set

```bash
# you should be able to use the environment variable set in the caller figaro for this if its called by a figaro
# if and only if that value is lost need you use the aria id from the form.
SELF="<your aria id from the form>"
WINDOW_MS=$((60*60*1000))   # e.g. last hour; adjust per user ask
NOW=$(date +%s%3N)
CUT=$((NOW - WINDOW_MS))

figaro list -j | jq -r --arg self "$SELF" --argjson cut "$CUT" '
  .[]
  | select(.kind=="conversation")
  | select(.id != $self)
  | select(.last_active >= $cut)
  | .id
'
```

That's your target list. Show it to the user before firing if there
are more than a handful, a courtesy, not a blocker.

### 2. Fan out in parallel

One `bash` tool call per aria, `background: true`, capturing raw
stdout. `-r` strips ANSI so the output is clean text ready for
synthesis.

```bash
figaro send --id <ARIA_ID> -r -- "<the user's question>"
```

Each becomes its own session. Record `{aria_id → session_id}`.

Do not use `-e` (that's for ephemeral throwaways, contradicts `--id`).
Do not use `-f` here: `-f` returns before the reply exists, and this
skill's whole point is *collecting* the reply.

### 3. Monitor and harvest

Poll the sessions. As each finishes, its stdout is that aria's
reply. Two useful signals:

- `process poll <session>`: status + output since last poll.
- Cross-check with `figaro list -j | jq '.[] | select(.id=="<id>") | .state'`, a `state` of `idle` means the daemon has finished the turn.

Kick off synthesis for early finishers while stragglers are still
going if that helps latency. Otherwise `wait` until every session
reports done.

### 4. Synthesize

Fold the replies into whatever the user asked for:

- **Chat**: a compact, attributed summary: "**a1b2c3d4** (mantra):
  their take" per aria, then a short comparative synthesis.
- **Document**: write a markdown file (or edit an existing one) with
  each reply quoted verbatim, followed by your synthesis.
- **Tool action**: interpret the collective as a decision and take
  the action (open a PR, send an email, run a command). Confirm with
  the user before any destructive action.

Always attribute. The user needs to know which aria said what.

## Failure modes to handle

- **Target went `dormant` mid-turn**: it errored or was killed. Note
  it in the synthesis; don't hide it.
- **Empty target set**: tell the user "no arias match that window"
  and offer to widen it.
- **A session hangs**: give it a generous timeout (a few minutes for
  a real reply), then note the timeout in the synthesis and move on.
- **Self appears in the list anyway**: bug in your filter. Fix it,
  don't paper over it.
