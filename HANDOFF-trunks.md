# HANDOFF — the presentation hierarchy: promote, ls, and delete

Worktree   `~/dev/figaro-qua/trunkfix`
Branch     `fix/trunk-presentation` @ `64c49d24`
Dev shell  `nix develop .#snapshot` (real config + credentials, dev-scoped state)

## figwal

This branch needs a figwal fix that is **not in a release yet**:
`a3bf1e9`, pushed as `jack-work/figwal` branch `fix/remove-subtree`.
`go.mod` names it by commit:

```
github.com/jack-work/figwal v0.16.1-0.20260812062559-a3bf1e987b64
```

A pseudo-version is fetchable, so `nix develop` and `nix build` work. A
local path `replace` is NOT: buildGoModule vendors the module set and go
refuses the mismatch. Do not reintroduce one.

Before a release: merge that figwal branch, tag it, and `go get
github.com/jack-work/figwal@vX.Y.Z`, then reset `vendorHash` from the
"got:" line nix prints.

## What was wrong

1. **promote had no reader.** It wrote `trunks.json`; `ls` rendered the
   fork topology. The verb reported success and changed nothing visible.
2. **promote had no ceiling.** It climbed through the outfit stump and
   the genesis root. `promote <id> 10` on a four-deep aria put every aria
   in the store under one aria, and set that aria's parent to "".
3. **A refused `kill` still wrote.** The boundary repair ran before the
   refusal, so a delete that said no had already detached a branch.
4. **A recursive `kill` kept only the founding node** (figwal): the
   descendants stayed on disk with a `.from` nobody could follow, and the
   index read them back as roots. This is why 109 of 503 conversations in
   the live store sit at the genesis root with no outfit above them.
5. **Nothing forgot anything.** `trunks.json` accumulated edges naming
   deleted arias.
6. **Absorbing a survivor's prefix teleported it to the root.**

## What to try

```sh
A=$(fig new -j | jq -r .aria_id)
B=$(fig fork --id $A --stay -j | jq -r .alternative)
C=$(fig fork --id $B --stay -j | jq -r .alternative)
fig ls -H -a          # A > B > C
fig promote $C        # was: silent no-op
fig ls -H -a          # now: A > C > B
fig kill $B           # refuses only if something is DRAWN under it
fig kill $B -r        # takes the history subtree; C survives under A
fig ls -H -a          # no orphan appears at the genesis root
fig promote $A        # refused: only conversations nest
```

## Numbers

Warm listing, 512 arias: 35.5 us trunkless, 35.6 us with a promoted
forest, identical allocations. A promote invalidates the snapshot; the
cold rebuild is 3.9 ms at 512 arias. The snapshot is keyed on figwal's
topology version AND a presentation revision, because a promote
deliberately moves no bytes.

## Left undone

- figwal is on a branch, not a release (above).
- The live store still holds the fossils of the old delete: 109
  conversations at the genesis root. Nothing here re-homes them; their
  lineage is gone from disk. `fig promote` can place them by hand now
  that promote works.
- `figaro doctor mem` on the live daemon reports 902 MiB heap against 24
  resident arias and a 45 MiB row cache. A sandbox of 301 arias sits at
  68 MiB RSS with no leak, so the difference is real arias' payloads,
  most likely the translation channels. Arm `FIGARO_PPROF=1` on the next
  daemon restart to settle it. Report: `/tmp/figaro-subagents/memory.md`.
- `feat/incantations` merges clean into this branch (test-merged, not
  guessed).

## Slated

`skills/figaro/contributing/trunk-singleton-form.md`: the hierarchy as
one unbound form per store, 1:1 with the angelus, and the one thing
figwal must grow for it (a channel whose retention is a single segment
rewritten whole).
