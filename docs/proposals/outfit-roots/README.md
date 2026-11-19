# Layered outfit roots

Figaro Engineer, 2026-11-19. Plan only; no code before Gluck approves.
Brief: `~/notes/spain/briefs/outfit-layering-and-split.md` (Gluck, 11-19 ~00:45).

## Today (v0.76.2)

There is one root, `<config>/outfits` (plus the legacy `loadouts/` fallback). One function walks it,
`config.OutfitFiles` (`internal/config/outfit_files.go:12`), and three callers use it: the
outfitter's index (`internal/outfit/resolver.go:156`), `Loaded.OutfitPath` and `Loaded.ListOutfits`
(`internal/config/config.go:907,935`). The outfitter records every directory it visits as a
dependency, so `figaro state outfit --refresh` and the stale window notice changes. `--tree`
draws the layer closure from the daemon (`internal/cli/outfit.go:240`). Layered roots do not
exist. They are cheap, because there is one walk to change.

## Proposal

1. **Ordered roots.** A new config key, `[outfits] roots = ["/etc/figaro/outfits", "~/.config/figaro/outfits"]`,
   lists the managed roots first and the user overlay last. With the key unset, behaviour is unchanged
   (one root, `<config>/outfits`). The charter renders the key per consumer.
2. **Resolution by name, last root wins.** `OutfitFiles` walks every root in order. The index maps
   each name to the root that won and to the roots it shadowed. Layers (`a,b` and the `layers` key)
   resolve each name through the same index, so a user overlay can shadow one layer of a managed outfit.
3. **Locked names.** A managed root may carry `.locked`, a list of names. Overlaying a locked name is
   refused: `--refresh` and `figaro doctor` name the file, and the managed version stays in force.
   That way an overlay can never silently change a managed outfit. Protecting single keys inside an
   outfit is not proposed. A whole-file lock is simpler and covers the merlin case, where the managed
   set is the security boundary (with #171, the grant names an outfit and the outfit carries the model).
4. **Provenance.** `--tree` prints each layer's root (`managed /etc/figaro/outfits/model/opus5.toml`,
   `user ~/.config/figaro/outfits/short.toml`) and marks a shadowed managed file. `state outfit`
   (the list) shows the same tag.
5. **Refresh sees both.** The walk visits every root, so every root's directories become dependencies.
   On merlin the managed root is a symlink into the store that changes on a rebuild; the dependency
   is taken on the resolved path, and the symlink itself is also statted, so a swap is noticed.
6. **Writes go to the overlay only.** Any figaro verb that writes an outfit (first run, `outfit
   --edit` if one lands) writes into the last root and never into a managed root. Managed roots are
   opened read-only.

## Per consumer (El Mayor's split builds on this)

- **gluck (home manager):** managed root = a store path rendered by HM (`~/.local/share/figaro/outfits-managed`
  as an out-of-store link, or straight from the store). User overlay = `~/.config/figaro/outfits`, editable at runtime.
  An HM rebuild changes the managed set; a runtime edit changes only the overlay.
- **merlin (NixOS, house mode):** managed root = `/etc/figaro/outfits`, a root-owned, read-only link into the store,
  so changing it takes a sudo rebuild. The overlay is the daemon's own config dir; whether house mode allows
  an overlay at all is Gluck's call (proposed: yes, with the charter's role outfits listed in `.locked`).

## What breaks

Nothing with the key unset. With roots configured, an existing user file with the same name as a locked
managed outfit stops applying, and doctor names it. The migration is to rename or delete it.

## Size

One lane: the walk, the index with provenance, the lock rule, `--tree` and list output, the refresh
dependency on symlinks, and tests (shadowing, lock refusal, refresh after a swapped symlink, writes never
reaching a managed root). About 300 lines with tests. Hours, not days. It can run beside #171, which it feeds.

## Open for Gluck

1. Lock by whole file (proposed), or per key.
2. On merlin, may the user overlay exist at all in house mode?
3. "Call it spain really" about desktop-system: what rename does he mean? (El Mayor asks; nothing is renamed before that.)
