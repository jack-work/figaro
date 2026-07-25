# board-default.json — provenance

`board-default.json` is a **verbatim capture of the real default-loadout
chalkboard** of this machine's figaro user, frozen so the benchmarks in
`chalkboard_bench_test.go` are hermetic and reproducible anywhere. Nothing
in the benchmark suite reads live config; it reads this file.

## What it is

37 keys, 15,776 bytes:

- 26 `skills.<base>` content envelopes (`{frontmatter, filePath}`) — the
  user's `~/.config/figaro/skills` plus the first-party skills bundled with
  the binary (`skills/` in this repo).
- `system.credo` — the real `~/.config/figaro/credo.md` envelope (4,768
  bytes, the largest single value).
- the `system.*` scalars: `provider`, `model`, `max_tokens`,
  `use_official_sdk`, `reminder_renderer`, `thinking_effort`, `cwd`, `root`,
  `loadout_name`, `loadout_version`, plus `aria_id`.

`system.cwd` / `system.root` read `/tmp/chalkcap` — the capture happened
there. `aria_id` is the throwaway aria's id. Neither affects what is being
measured.

**No credentials.** Provider auth lives in `~/.config/figaro/providers/*`
and in hush, never on the chalkboard; the capture was inspected key by key
and no key looked like a token, key, or secret, so nothing was dropped.

## How it was captured

Against an **isolated** daemon (never the user's live one), 2026-07-24, on
`chalk/bench` at fc29357:

```sh
mkdir -p /tmp/chalkcap && cd /tmp/chalkcap
go build -o /tmp/chalkcap/figaro <repo>/cmd/figaro

export FIGARO_RUNTIME_DIR=/tmp/chalkcap/run
export FIGARO_STATE_DIR=/tmp/chalkcap/state
# real config/skills are inherited; make the repo's bundled skills visible
# the way an installed binary would see share/figaro/skills
export FIGARO_BUNDLED_SKILLS=<repo>
mkdir -p "$FIGARO_RUNTIME_DIR" "$FIGARO_STATE_DIR"

id=$(./figaro new --loadout opus5 -j | jq -r .aria_id)   # no prompt => no turn, no tokens
./figaro state "$id" -j | jq -S . > <repo>/internal/chalkboard/testdata/board-default.json

./figaro rest                      # tear the isolated daemon down
rm -rf /tmp/chalkcap/run /tmp/chalkcap/state
```

`jq -S` sorts keys so the file diffs cleanly if it is ever re-captured.

## Re-capturing

Only re-capture if the fixture must reflect a changed loadout — and then
say so loudly in the report, because it invalidates comparison against any
previously recorded `bench-before.txt`.
