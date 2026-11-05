# Fuzzy search and a better form view

Scope: how the form view searches and draws. Access, view hints and actions
belong to pit-convergence (`duck/2026-11-05-pit-convergence.md`); this note
sits on top of it and needs nothing from it until stage 4.

## What the form view does today (v0.53.0)

`formView.Items` (`form_listen.go:279`) flattens the open branches into rows:
`label: preview` for a leaf, `label (n)` for a branch, two spaces per depth.
Search is the generic pit search (`picker.find`, `picker.go:91`): a
case-insensitive substring over the rows already on screen. A key inside a
collapsed branch is never found, nothing is highlighted, and there is no
ranking.

## Matcher: fzf's own algorithm, imported

Measured on this machine (benchstat-style, `-count 3`, 16 cores, one goroutine;
raw bench and corpus in `/var/tmp/fuzzy-bench`). Corpora: Gluck's own bound
form (140 leaves), `@models` (1,356), and synthetic forms shaped like his big
ones (skills with long frontmatter, comment threads, 400-word notes): 5k and
20k leaves. Time is one query over every entry.

| corpus, keys only | junegunn/fzf `algo.FuzzyMatchV2` | sahilm/fuzzy | lithammer/fuzzysearch |
|---|---|---|---|
| own form, 140 | 10 µs, 0 allocs | 32 µs | 37 µs |
| `@models`, 1,356 | 138 µs | 397 µs | 379 µs |
| synthetic 20k | 1.27 ms, 0 allocs | 4.8 ms | 4.7 ms |

Typing an 11-letter query key by key over 20k keys, narrowing the candidate
set each time: 4.7 ms for all eleven keystrokes together.

Fuzzy matching over values is the wrong tool: keys plus the first 512 bytes
of each value costs 69 ms per query at 20k (fzf) and 40 ms (sahilm), and
matches prose noise. A case-insensitive substring over cached lower-cased
values costs 3.8 ms at 20k and 6 µs on `@models`.

Ranking: for `systools`, fzf puts `system.tools.*` first; sahilm puts
`system.xwal.tools` first. fzf's scoring (word boundaries, `.` and `_` as
delimiters, contiguity bonus) fits dotted key paths.

Choice: import `github.com/junegunn/fzf/src/algo` (MIT). Compiled
dependencies: `fzf/src/util`, `go-shellwords`, plus `x/sys`, `go-isatty` and
`uniseg`, which figaro already has. Its API is not semver-promised (`algo.Init`
is global, `util.Chars` and `util.Slab` are internal types), so it sits behind
one small package, `internal/fuzzy` (`Match(query) []Hit`), pinned by a test.
If an upgrade breaks it, the fallback is copying `algo.go` and `chars.go` with
their licence. Not chosen: ktr0731/go-fuzzyfinder (a whole tcell UI, not a
matcher); nucleo (Rust).

## Index

One index per form view, built lazily on the first `/`:

- an entry per leaf: its key path as `util.Chars`, and its value as a
  lower-cased string (JSON strings unquoted, capped at 4 KB, with a flag
  when cut);
- kept current from deltas: `form.Patch.Entries()` names the touched paths,
  so a delta updates or removes only those entries. A resync rebuilds it;
- memory, counted on the corpora (keys as runes, values capped, per-entry
  overhead): `@models` about 290 KB, the 20k synthetic form about 15 MB. It
  is dropped when the view closes.

## Behaviour

Neovim's pickers are the model (telescope, fzf-lua, snacks.picker): a live
prompt, results moving on every keystroke, matched characters highlighted,
and a jump to the chosen item.

- `/` in the form view opens a prompt as the first row of the form's drawer,
  above the tree (transcript search keeps its own line at the bottom).
- On every keystroke the tree is filtered: rows that match, plus their
  ancestors, which open automatically. Matched characters are highlighted
  in the key; a value match shows a short snippet with the match
  highlighted. A count sits at the right of the prompt (`12/1356`).
- Ranking: key matches by fzf score, then value matches. The cursor sits on
  the best one; `Ctrl-n`/`Ctrl-p` and arrows move through matches in rank
  order, and the tree scrolls to keep the cursor in view.
- `Enter` closes the prompt and returns to the full tree with the cursor on
  that row. Only the branches leading to it stay open.
- `Esc` closes it and restores the tree as it was before `/`.
- `Tab` switches between the filtered tree and a flat ranked list of full
  paths, telescope-style.
- Syntax, as fzf has it: space-separated terms all match; `'term` is exact;
  `^` and `$` anchor; `!term` excludes; `k:` and `v:` limit a term to keys or
  values.
- Editing from a result: the cursor is on a real row, so `c` edits through
  the replica as today. A key the daemon marks read-only (pit-convergence's
  access) shows a lock, and `c` says why.
- Paste in the prompt goes to the prompt. The same routing fix as the search
  bug lane (2b86f6fd); this prompt uses that fixed input path.

## Drawing nesting more fluidly

- Guide lines (`│ ├ └`, dimmed) in place of bare indentation, as figtree
  already draws for `figaro ls`.
- Leaf values aligned in one column per branch, so a block of siblings reads
  as a table.
- A small branch (up to 3 scalar children, fitting the width) shows inline as
  `{a: 1, b: x}` and expands to rows on `Enter`.
- A collapsed branch peeks at its first children's names: `skills (14)
  brave, cheroot, …`.
- A breadcrumb above the tree names the cursor's path.

## Stages

1. `internal/fuzzy` and the index, with tests and the bench above as a
   committed benchmark. No UI.
2. The prompt in the form view: filter, highlight, auto-expand, rank
   navigation, `Enter`, `Esc`, in-place only. Byte-seam tests (keys and a
   bracketed paste) and a tmux run on a big form. It works on today's view,
   so it can ship before pit-convergence.
3. The drawing changes above, one at a time, each with a tmux capture.
4. With pit-convergence's view hint: list-shaped forms (`@models`,
   `@reaped`) match on their row cells and show results as rows.
5. The flat list (`Tab`) and the extended syntax.

Stages 1 and 2 fit one release. None of it touches access or actions, so
there is no El Inspector read unless stage 4 adds actions.
