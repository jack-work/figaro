# figaro demo tapes: scene catalog and run book

Goal: a catalog of short, scripted terminal scenes that showcase figaro's
abilities on camera. Style reference: the Obel Mono launch tape (60s, 1080p60,
132.5 BPM, hard cuts on a bar grid, scripted terminal sessions between bold
text cards). These are TAPES, not finished videos: each scene is a tmux
capture of the real binary in a real pty, driven by the smoke harness
discipline from the tmux-testing skill.

## Capture discipline (applies to every scene)

- Shell: `nix develop .#share-hush` from `~/dev/figaro-qua/main`. Real
  providers, isolated runtime+config+state. Prove with `type -a figaro`
  and `echo $FIGARO_STATE_DIR` before every capture.
- Start from an empty store per scene-set: `figaro stop`, wipe
  `$FIGARO_STATE_DIR/arias`, `figaro ls` must print zero.
- Panes spawn with a clean rc:
  `tmux new-session -d "bash --noprofile --init-file /path/to/panerc"`.
  Never let the interactive rc's figaro integration touch the store.
- One long turn auto-promotes to the transcript pager (trap 2 in
  tmux-testing): between takes, `q` then `C-c`, assert `$` before the next
  command.
- Model: pin a cheap real model in a demo outfit. Never a fake provider.
- Recording: tmux pane is the ground truth; capture with pane-pipe into
  `script`/ffmpeg at 60fps, 1920x1080, 60ms of terminal content per scene
  minimum. Asciinema-style replay is NOT used; we drive the real thing.

## Scene index

Each scene: title, feature beats, setup, script. Roughly 20-40s each, cut
down to 8-15s in the edit. Grouped into acts.

### Act I — One aria, doing work

**01. a turn.** Incipit loads. Prompt typed (visible typing), turn streams
in: footer alive, thinking, tool calls, prose. End on the turn sealed.
The "hello world" tape. Beat: a figaro is a conversation that feels alive.

**02. quoting.** Cursor parked on an earlier passage, yank quote coordinate
(`y` in visual), open a quote reply (`>` or the quote-reply flow), the
coordinate expands into the reply as `<aria.turn:lines>`. Send. Beat:
conversation is addressable; anything said can be answered in place.

**03. commenting.** `c` on a line, comment lands, navigate between
comments (`j`/`k` in comment mode), clear. Comments never reach the model.
Beat: annotation without interruption.

**04. forking.** Mid-conversation, fork at a turn (`f` prefix; fork marks
visible with `fork-next`/`fork-prev`), the fork incantation appears in the
branch's next turn. Explore the branch, come back. Beat: a conversation
that can branch and remember both sides.

**05. the tree.** Pit/list view: aria tree visible, `l` opens branches,
`h` closes, arrows walk. Parent with two forks visible; visit each.
Beat: the fork forest is one screen.

**06. splits and hopping.** Parent in one split, fork in another. Attend
with `a`, aria-back with `^O`, Tab forward, transcript-to-transcript
movement. Beat: many arias, one terminal, zero context loss.

**07. talking between arias.** Parent creates a git repo; children work
in child worktrees (treebear). Parent sends work with `send -f --id`,
children reply; parent polls a child's log, quotes the child's answer back
into its own turn. Beat: they actually talk to each other.

### Act II — Forms, roles, outfits

**08. the form pit.** `S` opens the form. `c` on a leaf fills the `:` line
with a `form set`; the edit is on screen at once, marked `pending` until
the server's delta lands. Toggle a key (`M-e`:
`:toggle system.methods.enabled`). Beat: state is a live, editable thing.

**09. attending forms.** `fig at @form`; an unbound form with no agent.
Patch it with `fig set`, watch deltas land (`fig form listen`), fork it
(`fig form fork @x who=trial`). Beat: durable state without a figaro.

**10. casting a role.** Mint a role form (`fig form new -S name=X`,
`fig set --id @x target-aria <aria>`). `fig send --id @x` reaches whoever
holds the role NOW. Repoint the role mid-scene to a second aria; the next
send lands on the new holder. Beat: roles are late-bound names.

**11. outfits in neovim.** Open `~/.config/figaro/outfits/<name>.toml` in
nvim (visible in a split). Edit the credo or skills. `fig outfit reload`.
Mint a new aria wearing it: the change rides the next `fig new`. Then dress
a live aria into it with `-O`. Beat: behavior is a file, hot and reloadable.

**12. selecting models.** The model picker from the UI. Spin the same
question through two models (two outfits, two arias, split view). Beat:
the model is a slot, not a commitment.

### Act III — Prediction, decisions, evals

**13. methods (autopredict).** Define `[system.methods.reasoning-effort]`
in nvim (visible edit). A turn arrives; the decision model reads the
inquiry, the pick lands on the form (`system.thinking_effort=high`),
the turn runs on the new value. `M-e` toggles methods off; the next turn
runs without it. Beat: a form key chosen for you, every turn, before you
see it.

**14. decide.** `figaro decide -C ship,revert,wait -- which way?` with a
quote coordinate as focus (`<412.0:23-1180>!`). Scored answer on screen,
probability readable. The decision pit in the UI if it exists. Beat:
a yes/no or a choice, with calibration, for a penny.

**15. driving evals.** A scripted transcript (a tape) replayed through
`figaro replay`; `decide` asked the same question at many points, scores
plotted (spawn a small Go script for the plot). Beat: the decision model
grades conversations cheaply and at scale.

### Act IV — Sandboxing

**16. the box.** `figaro box serve --socket /tmp/box --workspace ...` in
one pane. An aria with `system.sandbox` pointing at the box. Its turns
run inside the ephemeral box: it builds, tests, exits; only the
dependencies it needs come with it. Beat: dangerous work, disposable
rooms. Show the box refusing the daemon's own door, if that's the
mechanic; whatever ships is the truth.

### Act V — Polish

**17. animations.** Pacer, spinner, expanding content: stream a long turn,
watch content expand and settle. Freeze/scrollback, sticky scroll. Slow
it down. This is the beauty pass; shoot twice, cut to the beat.

**18. selection mode.** Visual mode: `v` mark-char, `V` mark-line, yank
coordinates, yank-id (`M-y`). Precise reference into the reply.

**19. hopping out of the shell.** `^D` detach, fg return, prompt intact.
Show the aria still running while the shell is elsewhere.

## Run book format

Per scene, a file in `tapes/scenes/NN-slug.sh` containing: environment
setup (store wipe, outfit pin), the keystroke script (driven through the
smoke harness or tmux send-keys per tmux-testing), the assertion(s) that
the take is good (pane content, prompt present, turn sealed), and the
raw capture directory. Takes are numbered; only reviewed takes move to
`tapes/takes/`.

## Then the edit

Music: pick a track, extract BPM, build a bar grid. Hard cuts on beats,
text cards between segments, 60s total. Terminal content carries;
no voiceover needed. ffmpeg for final render, audio muxed, 1080p60.
