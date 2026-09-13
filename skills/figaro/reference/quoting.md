# Quoting a passage, selecting it, and the pager's verbs

Read this when you want to point at something already said and talk about it:
from a shell, or with the cursor in the transcript pager.

## The token

A prompt that BEGINS with a coordinate token quotes that passage. The daemon
resolves it once, at the door, and what lands in the log is the quote, not the
token.

```
<412>!                 the whole message at lt 412
<412.0>!               block 0 of it
<412.0:23-1180>!       runes [23, 1180) of block 0
<412.0:23-419.2:88>!   from rune 23 of 412.0 to rune 88 of 419.2
```

- The `!` is the terminator, the same convention as `@key!`. Without it the
  text is literal and nothing rewrites it.
- Offsets are RUNE indices, half open, so a coordinate cannot land inside a
  character. A block index is not dressing: a message that says a paragraph and
  then calls a tool is one lt and two blocks.
- A coordinate that does not exist is REFUSED, with one sentence saying what
  the log holds instead, and nothing is queued or written.

From a shell, quote the token so your shell does not eat the `<`, `>` or `!`:

```sh
fig send --id 90ec6584 -- '<412.0:23-1180>! why did this branch return early?'
```

Where the numbers come from: `M-m` in the pager draws every node's address, or
read them from `figaro show <id> -v`.

## Selecting it in the pager

`v` puts a cursor in the transcript; `v` again marks by character, `V` by line.
vim motions move the cursor (`w b e`, `0 ^ $`, `H M L`, `{ }`, `h l`), and `/`
`n` `N` land it on a match.

| Key | Does |
|---|---|
| `v` / `V` | cursor; again to mark by character / by line |
| `y` | copy the selected text |
| `Y` | copy the selection's COORDINATE, `<lt.block:a-b>!`, for a shell |
| `:` | the command line, holding `<,>` when a highlight is up |
| `Esc` | drop the highlight, then the cursor |

`<,>` is this pager's `'<,'>`. It expands to the fully qualified coordinate
when you submit, and the coordinate is spliced in front of the prompt:

```
:<,>send -- is this the same bug as yesterday?
:<,>fork -- try it without the retry loop
```

The prompt after `--` is sent exactly as you typed it, spaces and all.

## The box's verbs

`:` runs figaro's own verbs, the same parser the shell uses, plus a coordinate
jump (`:12`, `:12.3`, `:0`).

| Line | Does |
|---|---|
| `:send [<id>] -- <text>` | submit; the transcript does NOT follow another aria |
| `:fork [<id>[:<turn>]] -- <text>` | branch, prompt it, ATTEND it and show it |
| `:fork --stay -- <text>` | branch and prompt it, attendance and screen untouched |
| `:listen <id>` | show that aria; the shell's binding does not move |
| `:attend <id>` | show it AND bind this shell to it |
| `:attend null` | go home: drop the binding, keep showing what is on screen |

The difference is the binding, and it is the only difference. `:fork` without
`--stay` does both, on purpose: no single command shows one aria while your
shell attends another.

Flags that mean another renderer or another process (`-x`, `-r`, `-v`, `-j`,
`-l`, `--record`) are refused by name here: the transcript is the stream.

Moving between arias:

| Key | Does |
|---|---|
| `a` | attend the aria the selected row (or the fork point on screen) names |
| `f j` / `f k` | next / previous fork point |
| `^O` / `^I` | jumplist: back / forward through the arias attended here |
| `M-m` | verbose tool output, and every node's address |
| `S` | the form in the pit |

## Settings

`[quote]` in `config.toml` governs what a quote looks like in the log:

```toml
[quote]
head_chars = 480   # runes kept from the front
tail_chars = 160   # runes kept from the end
ellipsis = "…"
gutter = "> "
header = true      # the "quoting aria … (N chars)" line
```

A passage longer than `head_chars + tail_chars` is sent as its two ends with
the ellipsis between; a shorter one is sent whole. Both zero is REFUSED (a
quote would be empty), and a negative value is refused by name. Zero on one end
is a real setting: `head_chars = 0` sends only the tail.

## What this does not do

- One quote per prompt, and it must be the first thing in the text.
- Tool ARGUMENTS are not quotable, only prose, thinking and tool output.
- A coordinate names a passage in the aria you are sending TO. Quoting another
  aria's message is not a thing you can do in one prompt.
- A selection whose messages have been evicted from the pager's window is
  re-read from the store when you yank it; the coordinate is stable either way.
