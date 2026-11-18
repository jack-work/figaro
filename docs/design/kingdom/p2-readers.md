# P2 readers of the moved keys

Moved: `system.forked_from`, `system.studies`, `system.tombstone`,
`system.checkpoint.marks`, `system.succession_warned`, `system.retired`,
`system.failure.{reason,category,turn,at}`, each to the same name under
`runtime.`.

Searched 2026-11-18 for the old names, the bare names, and any parse of
`figaro state -j`, `figaro get -j` or `figaro ls -j`: ~/dev/figscope,
~/dev/figscope-cockpit, ~/dev/paperd and the user units under
~/.config/systemd/user, ~/notes/spain/bin, ~/dev/gluck-herald,
~/dev/telefono, ~/dev/figla, ~/.local/state/impresario/scripts and tools,
/var/tmp/figstate, ~/dev/cheroot-config/charter, ~/.config/figaro (skills,
outfits), and this repo's skills/ and docs/.

## Outside this repo: no reader of a moved key name

| Reader | What it reads | Diff |
|---|---|---|
| figscope, figscope-cockpit | none of the keys | none |
| paperd and its notify units | none of the keys; no unit calls figaro state | none |
| ~/notes/spain/bin/fig-take-update | none of the keys | none |
| ~/notes/spain/bin/spanyard-watch | `figaro doctor ttl` text (`retired`, `reaped` columns) and `state -j` `target-aria` | none: the ttl listing comes off the _meta sidecar, not the key |
| ~/notes/spain/bin/domesday-grant.sh | `state -j` `target-aria` | none |
| herald (gluck-herald) | none of the keys | none |
| telefono | error text `aria is retired: ` from send | none: the refusal text is unchanged |
| figla | `get -j system.cache_control` | none |
| impresario scripts/stray-sidecars.sh | `_meta` sidecar `tombstoned_at_ms` | none: sidecar field |
| cheroot-config charter/scripts/figaro-root-check.sh | `ls -j` `retired_at`, `reaped_at` | none: listing fields, not keys |
| ~/.config/figaro skills and outfits | none of the keys | none |

## In this repo: docs that name a moved key

| File | Diff |
|---|---|
| skills/figaro/cli.md:307 | `system.retired = "context"` -> `runtime.retired = "context"` |
| skills/figaro/forms.md:65 | `system.studies` -> `runtime.studies` |
| skills/figaro/reference/architecture.md:379,398,402 | `system.studies` dropped from the box-held list (runtime class now); `system.retired` described as the request; a new "Runtime keys" paragraph |
| skills/figaro/reference/key-writers.md | regenerated from the registry |
| skills/figaro/reference/ui-stream.md:291 | `system.forked_from` -> `runtime.forked_from` |
| skills/figaro/reference/trunks.md:161 | none: `forked_from` there is a field of the fork reminder body |

## In this repo: code (changed in the same commit)

Every reader goes through `form.RuntimeGet`, `RuntimeHas`, `RuntimeLookup`
or `RuntimeEntry`, which read the new name and fall back to the old one for
one release: store (tombstone, checkpoint
marks, studies reconcile, lifetime sink, libretto mirror), angelus (birth
patch, box keys, topology), cli (formdeltas fork parent, forkjump,
turn_failure, doctor tombstones, retire), figaro (failure, study,
succession) and provider (fork render).

## Not moved, and why

- `turn.*`, `inflight`: on the status form, not the bound form; that form is
  runtime class already.

- `model`, `token_budget`, `truncation`: runtime rows at top level, not
  under system; left where they are.

## Moved in a later commit

- `system.reaped`, `system.reaped_until`, `system.reaped_by` ->
  `runtime.reaped*`: written only by kill, gc, the lifetime sweep and reaped
  restore, never by a caller. No reader outside the repo reads them; doctor ttl
  and ls take the reaped state off the _meta sidecar.
