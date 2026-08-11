---
name: tmux-testing
description: How to test figaro's terminal UI honestly: driving the real binary in a real pty with tmux, the traps that produced confident wrong answers, and the standing rules. Use when changing anything that paints (incipit, the transcript pager, the composer, footers, freeze/scrollback), when a test passes but a human reports a bug, or before believing any claim about what the screen shows.
---

# Testing figaro's terminal UI

> **Every test double that diverged from production diverged by being tidier
> than reality.**

That sentence was written after a single night in which **eight** green tests
certified broken code, and two bugs shipped anyway, a process that would not
exit, and a duplicated status bar: both found by a user in his own shell,
neither visible to the suite.

This skill is how not to repeat it.

## The rule that generates all the others

**A model of a terminal only knows the bugs its author imagined.**

Unit tests over `compose()` output tell you what figaro *decided* to paint. They
cannot see a row scrolling into history before the next repaint, a process that
never exits, a keystroke consumed as a motion, or a footer printed twice. If the
property you care about lives in the terminal, **test it in a terminal**.

## Run the smoke suite

```sh
FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v
```

`internal/cli/tmuxsmoke_test.go` is the harness; `tmuxsmoke_cases_test.go` is the
suite. Skipped by default so `go test ./...` stays fast and hermetic. Each case
names the shipped bug it exists to catch: read those before adding one.

**It uses a real provider on purpose.** A fake provider would be one more double
free to drift. Costs tokens; a pass means something.

## Use a dev shell: it is the harness you keep re-inventing

```sh
nix develop .#share-config   # isolated runtime+state, YOUR config + hush
```

Inside it, `figaro`/`fig`/`q` are **this worktree's build, already stamped**
(`flake.nix` passes `-X …internal/cli.commit=${rev}`), and
`FIGARO_RUNTIME_DIR`/`FIGARO_STATE_DIR` are dev-scoped, so nothing you do
touches your live daemon or arias. `duck/HOW_TO_TEST.md` is the long version.

Presets: `share-config` (real providers work: use this), `clean` (hermetic,
fresh hush + first-run), `snapshot` (a copy of your real arias), `share-hush`,
`sandbox`, `default` (everything inherited).

Exporting `FIGARO_*` by hand instead is how you end up talking to **another
build's daemon**: set the three vars in a tmux pane, and an interactive shell's
own integration can still run the installed `figaro`, which autostarts a daemon
of ITS version inside your isolated runtime dir and answers your new client.
That happened. The pane env was correct and the daemon was wrong.

The shell warns about the other half of this trap in its own banner: a shell
launched inside the dev shell may re-prepend `~/go/bin` and shadow
`$FIGARO_DEV_BIN`. Re-prepend it in your rc when it is set.

## Capturing demo material: a devshell on a NEW store

The dev shells isolate runtime and state, but a dev root **persists across
entries**, so "isolated" is not the same as "empty". When the output is going
somewhere public: a demo, a screenshot, a marketing page: start from a store
with nothing in it, and prove it twice.

```sh
cd ~/dev/figaro-qua/main
nix develop .#share-hush        # real credentials, isolated runtime+config+state

# 1. prove which binary and which store the shell actually has
type -a figaro                  # the dev bin must be first
echo "$FIGARO_STATE_DIR"

# 2. start from empty: stop the dev daemon FIRST, or it rebuilds the store
figaro stop
rm -rf "$FIGARO_STATE_DIR/arias"
figaro ls                       # must print 0 top-level arias

# ... capture ...

# 3. prove it again on the way out
figaro ls                       # only what you made
FIGARO_STATE_DIR= FIGARO_RUNTIME_DIR= FIGARO_CONFIG_DIR= \
  ~/.nix-profile/bin/figaro ls  # the real store, untouched
figaro stop                     # trap 10: the scratch daemon is yours to reap
```

`share-hush` is the preset for this: it shares the global hush identity (so
real provider credentials work) while isolating runtime, config **and** state.
Because the config is a copy, outfits can be edited freely during a capture -
which is how the outfit demo is filmed - without touching the real ones. Pin a
cheap real model in a demo outfit rather than reaching for a fake provider:

```toml
# $FIGARO_CONFIG_DIR/outfits/impresario.toml
layers = ["default"]
[system]
provider = "anthropic"
model    = "claude-haiku-4-5-20251001"
```

**Three traps specific to capture work**, all of which cost real time:

1. **A tmux pane inherits the user's interactive rc**, and a figaro prompt
   integration in it will run the INSTALLED figaro against your isolated
   runtime dir: which autostarts a daemon of the wrong build, and then every
   command in the pane fails the build handshake. Spawn panes with a clean rc
   that only sets the dev env:
   `tmux new-session -d "bash --noprofile --init-file /path/to/panerc"`.
   Note `--norc` CANCELS `--init-file`; passing both silently gives you neither.
2. **A long turn auto-promotes to the transcript pager** (trap 3), and the next
   command you send is typed INTO the pager rather than the shell. Return to
   the prompt between scenarios (`q`, then `C-c`) and assert you see `$` before
   sending anything else.
3. **`pkill -f <pattern>` matches your own command line.** A cleanup step
   spelled `pkill -f figaro-dev-share-hush` killed the very shell that was
   running it, halfway through, leaving the store un-wiped and the operator
   convinced the wipe had run. Reap by pid, or by an absolute exe path.

## Build a stamped binary: when you build by hand

The dev shell stamps for you, and so do `scripts/*.sh` and the harness
(`smokeBinary`). Reach for this only when you need two binaries at once (an A/B
comparison):

```sh
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)" \
  -o /tmp/figaro ./cmd/figaro
```

A plain `go build` **in a git worktree records no revision at all**: Go's VCS
autodetection only fires when `.git` is a *directory*, and a worktree's is a
file. `-buildvcs=true` does not help and does not complain; it just stays
silent. So `figaro --version` says `unknown`, and the CLI/daemon build
handshake (`checkDaemonBuild`) has nothing to compare: it can only warn. That
is how an old daemon spoke a new client's wire and rendered a user's own
question in figaro's voice, four times, with no error.

## Eleven traps, each of which produced a confident wrong answer

**1. `tmux new-session -y N` gives pane height N−1.** The status bar takes a row,
and turning it off afterwards does **not** give the row back to a detached
session: nor does `resize-window`. Measured: `-y 30` → 29 either way; `-y 31` →
30. So **ask for `h+1`**, then *read back* `#{pane_height}` and report **that**.
An entire thread of "h=1 loses the reply": three investigators, many trials -
was measured at pane height **zero**, a state no user can reach.

**2. Counting a token across the capture is unsound.** The footer mantra echoes
the prompt, the prompt usually contains your token, and an 80-column shell echo
wraps so its tail fragment matches too. Every trial inflates by two, which once
made a live bug look fixed. Only the rendered body line is sound:
`grep -cE '^[[:space:]]*<TOKEN>[[:space:]]*$'`.

**3. Gate every ABSENCE on pager chrome.** Grep for `? help`, `! status`, or an
`N–M/T` range. A long turn **auto-promotes** to the pager, where earlier content
sits above the tail window: so it isn't in your capture. *An absence inside a
pager is not an absence.* This produced two false reports and cost two agents
real time. Use a pane tall enough not to promote (`y=100` for tool-heavy turns)
and assert `pagerChrome == 0` **before** trusting any count.

**4. Capture scrollback, not the pane.** `capture-pane -p -S -`. Frames that
existed for milliseconds are preserved there verbatim. This is how the
submit-time footer stanza was photographed after fast polling failed to catch
it: the right instrument was a *short pane*, not a faster loop.

**5. Type one character per read.** `send-keys -l "whole string"` arrives as a
single read; a human types one byte at a time. Composer tests fed whole strings
and passed while a byte-vs-rune bug mojibaked every non-ASCII character. **If
the thing under test is input, type slowly.**

**6. Test the path of someone who does not know the affordance exists.** Three
investigators tested the composer by pressing its trigger key first, and all
three pronounced it sound. Nobody typed *without* it: which is exactly where
silent, partial, meaning-changing input loss lived. *Every expert test agreed
with every other expert test and all of them were incomplete.*

**7. Poll until stable; never sleep a fixed guess.** A model's first token can
take five seconds or fifty. `waitIdle` returns when the pane stops changing.

**8. A naive duplicate-line check misses real duplication.** The body-duplication
bug placed its two copies ~25 lines apart, separated by re-rendered thinking and
tool output. Compare *sequences*, not adjacency.

**9. Verify the model complied before blaming the renderer.** A wrapped
paragraph defeats a `line == token` regex. A model-set mantra containing your
token inflates counts. Both produced false positives.

**10. Clean up the daemon, not just the session.** `kill-server` leaves the
scratch daemon running. Seventeen agents each left one: **230 orphaned
processes**, 1.2 GB of tmpfs, and a memory-pressure alert with processes already
stalling. The harness's `close()` does both: if you drive tmux by hand, you
must too.

**11. `tmux new-session -e PATH=...` is silently ignored: so your A/B runs the
SAME binary twice.** Measured on tmux 3.7b: `-e MYCANARY=hello` arrives intact
while `-e PATH=/tmp/build:...` does not, leaving the pane on the inherited
`~/.nix-profile/bin`: i.e. the INSTALLED figaro, not the one under test. An
agent A/B'ing a fix this way got byte-identical "before" and "after" output and
reported the fix as having no effect; the arms had never differed. Nothing was
wrong with the fix and nothing was wrong with the assertions.

Two defences, use both:

```sh
# 1. invoke by ABSOLUTE PATH; never rely on PATH inside a pane
tmux send-keys -t s '/tmp/ab/after --version' Enter

# 2. or export INSIDE the shell, which does work, then PROVE it
tmux send-keys -t s 'export PATH=/tmp/ab:$PATH; type -a figaro' Enter
```

Assert the binary before you assert the behaviour: `type -a figaro`, or an
`md5sum` of each arm quoted in the report. Two arms that produce identical
output are more often one binary than one bug.

Better still, when the test does not actually need a terminal: **don't use
tmux.** Pid-binding tests, exit codes and stdout shape can run as plain
subprocesses, where the binary path is explicit and there is no environment to
smuggle.

## Timing recipe for steering bugs

A steer must land **after a tool has completed** or there is nothing to
misorder. A steer fired before the first tool shows nothing wrong, and that
single difference produced two contradictory bug reports.

```
t=0   figaro new -- "run 4 readonly bash commands with sleep 8 between each, then say DONE"
t=14  fire the steer   <- the whole trick
t=72  capture-pane -p -S -
```

Assert: `pagerChrome == 0`; screen `✓ bash` count **equals**
`show --json | [.parts[].nodes[]|select(.type=="tool")] | length`; `↳ input == 1`;
and **the node ORDER on screen matches `fig show`**: incipit hoisting a steer
above the tools while `show` places it correctly is a live-vs-committed
divergence, which the purity invariant forbids.

## Before you believe a test

- Did it ever fail? **Canary it**: revert the fix and quote the failure. An
  assertion that has never failed is not evidence.
- Does the double call the real function? The paging double treated `limit` as a
  *count* while production used a *byte budget*, and never called `Paginate`.
  ~30 tests green while the pager rendered one node for an 800-node aria.
- Does the fixture still exercise its own path? One test had silently stopped
  scrolling and could no longer fail for its stated reason.
- Is it pinning current behaviour or intended behaviour? Two tests *asserted the
  bug*: one expected a tool present in the IR to render nowhere; a golden file
  literally encoded a duplicated voice marker.

## Twelve arias, one daemon, one build

The suite proves one client at a time. The failures that cost releases were
concurrent: twelve shells minting, dressing and prompting arias through ONE
daemon, each in its own nix devshell over the same build. Not a Go test: it
spends tokens and it needs the real provider: so it is a recipe, run by hand
before anything that touches the store, the form writer or the inbox lands.

```sh
nix build .#default -o /tmp/fig            # the build every shell shares
export FIGARO_STATE_DIR=/tmp/stress/state FIGARO_RUNTIME_DIR=/tmp/stress/rt
for i in $(seq 1 12); do
  ( nix develop --quiet --command bash -c \
      "/tmp/fig/bin/figaro send -O mantra=stress-$i,ttl=$i -r -- \
        'reply with the single word ok' > /tmp/stress/$i.out 2>&1" ) &
done
wait
```

Then read, in this order: each answers a different question:

- `grep -l ok /tmp/stress/*.out | wc -l`: did all twelve get an answer? A
  serialization bug shows up here as a hang, not a wrong answer.
- `figaro ls -a`: twelve DISTINCT ids, each with its own mantra and 2 messages.
  A shared-state bug shows up as a repeated mantra or a missing row.
- `figaro state --id <one>`: the form is materialized: `-O` names became keys,
  and `layers` is not on the board.
- `awk '/VmRSS/{print $2}' /proc/$(cat $FIGARO_RUNTIME_DIR/angelus.pid)/status`
: the daemon's own footprint, not the harness's.

Measured on the form branch: 41.7s wall for twelve, 12/12 answered, 56 MB
daemon RSS, 1.5 MB store. Isolate all three of `FIGARO_CONFIG_DIR`,
`FIGARO_STATE_DIR` and `FIGARO_RUNTIME_DIR` or the run finds the user's daemon -
an interactive shell's own prompt integration will start one for you.
