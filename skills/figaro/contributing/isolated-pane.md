# The isolated pane: a TUI hunt that cannot touch anything of the owner's

**When to read this.** You are about to look at, or change, something figaro
DRAWS, and you need the real binary in a real terminal. Also read the
**tmux-testing** skill (the environment traps) and [ui-testing.md](ui-testing.md)
(the procedure: oracles, phases, promotion to a Go test).

The one rule from [maintaining.md](maintaining.md) still governs: **never test
against the live daemon.** This file is how you obey it without giving up a real
pty, a real dev-shell build, or a real turn.

## The pattern, in one sentence

One `/var/tmp` unit directory holds an isolated store, config, hush and tmux
SOCKET; the pane inside that private tmux server runs `nix develop`, so the
`figaro` under test is the flake's own build and not a `go build`; and the
provider is a local python script, so a turn costs nothing and says exactly
what the fixture needs it to say.

```sh
source scripts/devpane.sh
dp_init quote                   # unit dir, fake gateway, dev-shell figaro
dp_reply default <<< "a paragraph long enough to quote"
ARIA=$(dp_new)
dp_fig send -f --id "$ARIA" -- "a question"
dp_wait_idle "$ARIA"
dp_pane 100 40                  # a pane INSIDE `nix develop .#clean`
dp_run "figaro listen $ARIA"
dp_key C-t; dp_stable; dp_cap   # what the terminal kept
dp_down                         # tmux server, daemon, gateway, unit dir
```

`DP_SHELL` picks the preset (`.#clean` by default). `DP_KEEP=1` keeps the unit
dir, so a second run reuses the store instead of minting a new aria.
`dp_verify_clean` after `dp_down`.

## What each piece is isolating, and from what

| Knob | Set to | Protects |
|---|---|---|
| tmux `-S <unit>/tmux.sock` | a private server | `kill-server` can never reach the owner's sessions |
| `FIGARO_RUNTIME_DIR` | `<unit>/run` | the live daemon's socket, PID and bindings |
| `FIGARO_STATE_DIR` | `<unit>/state` | 130 MB of the owner's real conversations |
| `FIGARO_CONFIG_DIR` | `<unit>/config` | his outfits, skills, credo, and `providers/` |
| `FIGARO_HUSH_APP` + `_DIR` + `_PASSPHRASE` | `<unit>/hush` | his real credentials and keyring entries |
| `FIGARO_DEV_ROOT` | `<unit>/dev` | the dev shell's `bin/figaro` symlink, which is per PRESET, not per worktree |
| the provider | `127.0.0.1:<free port>` | tokens, and the model's weather (see below) |

The last two are the ones a hand-rolled script gets wrong.

**The dev root is a global.** `nix develop .#clean` puts `figaro` on PATH as
`$FIGARO_DEV_ROOT/bin/figaro`, a symlink into `/nix/store`, and the default dev
root is keyed to the PRESET NAME: `$XDG_RUNTIME_DIR/figaro-dev-clean`. Two
worktrees in the same preset therefore fight over one symlink, and the loser is
running the other worktree's build while `--version` agrees with both. The
shellHook now honours a pre-set `FIGARO_DEV_ROOT`, the way every other knob in
`mkFigaroShell` already did; `dp_init` sets it per unit. If you drive a dev
shell by hand, set it.

**A fake provider is not a lesser test, it is a different axis.** The smoke
suite drives a real model and reports fourteen ways to DECLINE, every one of
them produced by the model answering too fast, not calling the tool, or
producing a reply long enough to promote the pager (`plans/smoke-skip-profile.md`).
A hunt into chrome does not want that weather: it wants a paragraph of a known
length, every run. `scripts/fake-gateway-prose.py` serves `<replydir>/<n>.md`
for request n, so `dp_reply` decides what the turn says. Anything about
provider behaviour itself still belongs on the real thing.

## The traps this harness exists to carry

Each one cost a cycle, and each is a line of `scripts/devpane.sh` you would
otherwise have to rediscover.

1. **`nix develop` builds from the GIT TREE, so an untracked file does not
   exist.** A new `.go` file you have not `git add`ed makes the shell fail to
   build with "undefined: …" naming your own new symbol, which reads exactly
   like a typo in the code you just wrote. `git add -A` before entering a
   shell, every time; it does not have to be committed.
2. **`tmux new-session -e VAR=…` is silently ignored.** Export in the shell
   first, then enter the dev shell from that shell, so the preset inherits the
   knobs. `mkFigaroShell` uses `: "${VAR:=…}"` for exactly this.
3. **A pane is not the height you asked for.** tmux may charge the session a
   row for the status bar and will not give it back when the bar goes off.
   `dp_pane` resizes until `#{pane_height}` is the number you asked for, and
   says so loudly when it cannot. Assert against what it printed.
4. **An isolated hush with no passphrase stops every command.** `figaro new`
   dies with "needs a controlling terminal, but stdin is not a TTY" the moment
   `FIGARO_HUSH_DIR` names a vault that does not exist yet. Generate one
   per unit and export `FIGARO_HUSH_PASSPHRASE`: figaro honours it ONLY when
   `FIGARO_HUSH_DIR` is set, so it can never reach the real keystore.
5. **First run wants a human.** `interactive = false` plus a `default_outfit`
   in the unit's `config.toml` is what keeps the outfit picker off the pane.
6. **A daemon's argv does not say which store it opened.** Attribute by
   `/proc/<pid>/environ`, not by `pgrep -f figaro`, or teardown either misses
   the daemon it started or kills one it did not.
7. **Scratch belongs on `/var/tmp`, not `/tmp`.** `/tmp` is tmpfs here: RAM.

## Where this sits next to the other harnesses

| Harness | Reach for it when |
|---|---|
| `scripts/devpane.sh` | you need a dev-shell binary, a private pty and a turn you control: looking at chrome, a quote, a footer, a card |
| `scripts/paintpane.sh` | hunting one paint bug with the packaged oracles (`pp_jogdiff`, `pp_gapcheck`); it builds with `nix develop .#tools --command go build` and drives the binary directly, which is faster and one step further from the flake |
| `internal/cli/tmuxsmoke_*_test.go` | the automated end, real provider, `FIGARO_TMUX_SMOKE=1` |
| the VT model (`newVT`) | the property is about what figaro DECIDED to paint, not about what happened in a terminal |

A capture is evidence of what a terminal kept. It is not an oracle: write the
predicate before you drive (ui-testing.md §2), and when the answer is worth
keeping, promote it to a Go test against the shared composer, which is cheaper
than a pane and runs on every commit.
