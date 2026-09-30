# Notifications: what figaro told you, still there after it stops saying it

Status: design, not built. Branch `plans/messages`. Named NOTIFICATIONS (Gluck, 2026-09-30), which is also the name the pit already carries (`pitNotifications`).

## The problem

Everything figaro says to the pager goes through one slot: the first cell of
the status bar (`sessionStatus.setNoticeAt`). It holds for `notice_ttl`
(10s) and then it is gone. A turn that failed while you were reading
something else, a `:attend` typo, a provider refusal: once the slot retires,
the only trace is `sessionStatus.lastError`, which keeps exactly one string,
and the daemon's log file, which nobody opens mid-conversation.

There is no list. There is a glyph for one (`pitNotifications`, `𝄞`, with
`♩` as its selection cue, in `pitid.go`) and nothing opens it.

## What was already decided

`plans/status-bar-and-modes.md` §4 (2026-08-28) designed this and ordered it
last in its build list. Steps 1 to 5 shipped: the alert slot that retires on
the 11 Hz ticker, the one picker every pit conforms to, `AndMore`. Step 6,
"notifications, the only new surface", did not. Its decisions stand and this
plan keeps them:

- `𝄞` is the glyph, `♩` the selection cue.
- The alert is transient in the bar and is not tied to the list being read.
- It is a picker like every other pit: `^N`/`^P`, `y` yanks, **no delete**
  ("a log you can edit is not a log").
- Phase 1 is CLI-local and says so: a pager cannot see the daemon's `slog`.

What it left open, and this plan answers: the transport for daemon and agent
trouble, levels, an unread mark, and what a row is.

## How others do it

**Vim and Neovim.** Two faces of one store. A message is echoed where you are
looking and retires; `:messages` shows the history (500 entries by default in
Neovim, `messagesopt=history:N`), `:messages clear` empties it, `g<` re-shows
the last output. Every message has a kind (`emsg`, `wmsg`, `echomsg`,
`lua_error`, ...) and the kind decides its colour. Neovim 0.12's ui2 makes
the split explicit: a small `msg` window that times out, and a `pager` window
for history and for anything too long to glance at.

**Gluck's own Neovim.** The real spec. `config/msg_router.lua` routes every
message kind to `vim.notify` with a level (error, warn, info) and
**coalesces**: a message whose text matches a known pattern, or that Neovim
flags as replacing the last, updates one toast in place instead of stacking
(holding `u` does not bury the screen). `snacks.notifier` shows it for 3s and
keeps it; `<leader>n` opens the history as a list.

**tmux.** `display-message` in the status line, `show-messages` (`prefix ~`)
for the log of them, with timestamps, capped by `message-limit`. The same two
faces, in the tool figaro lives inside.

**VS Code.** A toast that retires, and a bell with an unread count that
opens the list. The count is the part worth taking: it is how an error that
retired unread stays findable without holding the bar.

## Why not a log viewer

A log is for whoever debugs figaro: every record, every attribute, debug
level, three processes interleaved. Messages are for whoever uses it: what
figaro told you, and what went wrong that it should have told you. Sixty
lines of cache bookkeeping between two turn failures is a log doing its job
and a message list failing at its.

So the list holds messages, and a message may carry the record it came
from: a row expands to the full text and the record's attributes (`aria`,
`err`, `lt`, ...). That is the one piece of a log viewer worth having, and it
costs nothing more than keeping the attributes. The whole log stays where it
is: `logs.jsonl`, `figaro doctor`.

## The design

### A message

```go
type message struct {
	seq    uint64    // ordering and the pit's row id
	at     time.Time // the first time it was said
	last   time.Time // the latest, when coalesced
	count  int       // how many times: "×3"
	level  msgLevel  // info, warn, error
	source string    // "cli", "daemon", or an aria id
	text   string    // one line: what the bar showed
	detail string    // the full text, when the bar showed a clipped line
	attrs  []attr    // the slog record's attributes, when it came from one
	key    string    // coalescing key; empty never coalesces
}
```

Levels are three, as in Gluck's router: **error** (red, counts as unread),
**warn** (yellow, counts), **info** (gray, does not). `alertLevel` grows the
missing middle.

**Coalescing**, from `msg_router.lua`: a message whose `key` matches one
already in the history does not add a row. It bumps `count` and `last` and
moves to the top. The key is `source + level + text` with digits folded, so
"retry 1 of 3", "retry 2 of 3" is one row that says ×3. A poller that fails
every tick produces one row that counts, not three hundred.

### Two faces, one store

The history is the store. The bar is one view of it and the pit is another,
and neither owns anything. That removes `lastError`, which becomes a query:
the newest error in the history.

1. **The alert** (unchanged): every new message takes the bar's first slot
   and retires after `notice_ttl`. Posting to the store IS posting the alert;
   `setNoticeAt` becomes `post(message)`.
2. **The mark** (new): after the alert retires, the bar keeps `𝄞 2` while
   there are unread warnings or errors, coloured by the worst. Opening the pit
   reads them all. Info never counts, so confirmations like `sent` never
   leave a mark. This is the answer to "it vanishes": it vanishes from the
   bar's first slot, and stays counted until you look.
3. **The pit** (new): `𝄞 notifications`, a picker like every other pit.

### The pit

```
𝄞 notifications · 2 unread                                   all ▾
  14:02:11  ✗  e8694d25  turn failed: provider refused: overloaded      ×2
♩ 14:01:40  ⚠  daemon    hibernation: restore of 3b7aff0a took 4.1s
  13:58:02  ·  cli       sent to 1d7afbc5
  … 41 more
```

- Newest first. The picker is a window; the history is not truncated to it.
- A row is time, level glyph, source (short aria id, `cli`, `daemon`), text,
  and a count when coalesced. Descriptions clip with the tab pit's rules
  (`clipTail`), since the text is prose.
The keys follow the keymap's standing rule: "'a' is attend everywhere; 'e'
is expand everywhere", and Enter is the pit's own action.

- **Enter**, the pit's own action, and **`e`**: expand the row in place, full
  text then `key: value` per attribute; again to collapse.
- **`a`** attends the row's source when it is an aria, as `a` does in every
  pit.
- **`y`** yanks the full text and attributes.
- **`f`** cycles the filter: all → warn and up → errors.
- **`/`** searches, as in every picker.
- Opening marks everything read. There is no delete, per the earlier
  decision; `:notifications clear` empties the pager's own copy (Vim's `:messages clear`),
  and says so.

**The key.** Gluck asked for `n`, which is taken: `n`/`N` repeat the last
search and land the cursor, as in Vim, and `N` has no other meaning to fall
back on. Proposed instead: **`space n`**, a two-key prefix like the existing
`gg` and `f j`. Space is unbound in the transcript, and `<leader>n` is exactly
the chord Gluck's Neovim opens its notification history with (snacks,
`show_history`). `:notifications` (and `:no`) open it from the command box.
If a space leader is wanted for more later, it starts here.

### What there is to notify about

Asked honestly, "is there anything besides errors?": not much today, and
more that should be.

**Said today, and lost after ten seconds** (phase 1):

- *errors*: a verb that failed (`:attend` a bad id, `queue rm` refused), a
  turn that failed and why.
- *refusals*, which read as warnings: "no fork point above", "nothing to
  undo", "this session cannot attend".
- *confirmations*, info: "yanked", "edited", "sent", "attending 3b7aff0a",
  "forked … prompting …".
- the CLI process's own `slog.Warn`/`slog.Error` (17 sites), which today reach
  a log file and not the person at the CLI.

All of it goes through about 60 `setNoticeAt` / `setCommandNoteAt` /
`noteErr` call sites that already pick a level, so phase 1 is a change of
destination, not of callers.

**Happening today, and never said at all** (phase 2, and the reason it is
worth doing):

- a turn finishing on an aria you are **not** looking at: a child you
  spawned, an aria you attended earlier this session. Done, interrupted,
  failed. This is the notification a person running several arias actually
  wants, and nothing surfaces it.
- a queued message **dropped** (the `dropped` pit exists; nothing says a
  message went there).
- another aria **writing to** yours (visible only by scrolling to it).
- the daemon: an aria hibernated or restored slowly, a provider refusing, a
  build mismatch between CLI and daemon.
- an **update** available (`figaro update --check` knows; nothing asks it).

**Phase 2 transport, the daemon's**: one daemon-level stream, not a form per
aria (see Where they live). The nearest existing machinery is the intrinsic
form (`<aria>/queue`, `<aria>/runtime`): derived, live-only, pushed to every
subscriber, exactly this lifetime. Today every intrinsic is hosted by an
aria; this one is hosted by the angelus, `angelus/notifications`. Whether an
intrinsic can take the angelus as its host, or needs a sibling mechanism with
the same shape, is the first thing phase 2 finds out. It is fed by:

- turn verdicts for every aria (the `turn.done` reason that is currently a
  bar string and nothing else); the pager keeps the ones for arias it has
  shown;
- queue drops;
- provider round failures, including filtered and empty completions (#24);
- the daemon's `logring`, which already retains WARN and above for
  `figaro doctor provider`; the 20 of its 44 warn/error sites that carry an
  `aria` attribute are tagged with it, the rest are the daemon's own.

The pager folds these into its history as it does the queue, tagging each
with its aria as the source and filtering to the arias it has shown. For
free: `figaro form listen angelus/notifications` shows the same stream from a
shell, and `figaro form show angelus/notifications -j` reads it from a
script, which is the rule the tab pit followed: one source, and the pit is
only a face.

**Not in either phase:** durability. The daemon's stream dies with the
daemon, as the queue does. What must survive a restart already does
(turn failures are in the IR; everything is in `logs.jsonl`). If a history
that outlives the daemon turns out to be wanted, the form becomes a libretto
(durable, derived) and nothing above it changes.

## Build order

Each step lands green on its own, with a pty check (`scripts/messages-pty.sh`,
in the style of `tabpit-pty.sh`).

1. **The store and the post**: `message`, the ring (500, `messagesopt`'s
   default), coalescing, `warn` level. `setNoticeAt` posts into it; the bar
   reads its newest. `lastError` goes. No visible change yet except that
   nothing is lost.
2. **The pit**: `space n` and `:notifications`, the picker, Enter/`e`/`a`/`y`/`f`, mark-read
   on open. `pitNotifications` finally opens.
3. **The mark**: `𝄞 N` in the bar after the alert retires, worst level's
   colour, cleared by opening.
4. **CLI `slog`** into the store.
5. **Phase 2**: `angelus/notifications` in the daemon, fed by turn verdicts,
   queue drops, provider failures and `logring`; the pager folds in the
   arias it has shown.

Steps 1 to 4 are one PR (the pager's own notifications). Step 5 is its own.

## Where they live

Split by where a notification is born. Nothing new is written to disk.

- **The pager's own** (confirmations, refusals, failed verbs, the CLI's
  `slog`): **in the pager process**, a 500-entry ring. It dies with the pager,
  which is right: "yanked" means nothing to the next session, and a store
  shared through the daemon would put one pane's confirmations in another
  pane's list.
- **The daemon's** (turn verdicts, queue drops, daemon trouble): **in the
  daemon**, a bounded ring behind one daemon-level intrinsic form, pushed to
  the pager over one subscription as the queue is. It survives the pager
  closing and dies with the daemon. One subscription rather than one per
  aria: turn verdicts are rare, and the pager filters them itself.
- **Scope is the pager's**: it keeps the set of arias this session has shown
  (the subject and every aria hopped to, attended or spawned), and shows the
  daemon's notifications for those. A new session starts with its first
  aria.
- **Disk**: none. The durable facts are already written down, a turn's
  failure in its aria's IR and everything in `logs.jsonl`, so the list is an
  index of recent trouble and not a second record. Should it need to outlive
  a daemon restart, the daemon's ring becomes a libretto (durable, derived)
  and nothing above it moves.

## Settled (Gluck, 2026-09-30)

- **Name**: notifications.
- **Key**: `space n`, a two-key prefix like `gg`; `n` stays search-repeat.
  `:notifications` from the command box.
- **Info enters the history** and never counts toward the unread mark.
- **"A turn finished elsewhere"** covers the arias this pager session has
  shown, not every aria the daemon runs.
- **Storage** as above: pager memory for its own, daemon memory for the
  daemon's, nothing new on disk.

## Open

1. Phase 2 now, or after phase 1 has been lived with?
