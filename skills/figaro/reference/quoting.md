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

## What it looks like afterwards

What lands in the log is a heading naming the passage, then the passage under
the gutter, then your own words. The heading leads with the canonical
coordinate, so the terminal reads it back instead of guessing:

```
> quoting <412.0:23-1180>! · aria 7e151902 · turn 9 · 1157 chars
> Ecco fatto. The barber works in three strokes, …
```

Every surface that draws a conversation (the pager, the inline view, `show`)
draws that block as a CARD rather than as a paragraph of markdown: one dim
heading row with the coordinate, three rows of the passage under a rule, and a
closing row counting what is folded. Select the question (`^N` / `^P`) and
press `Enter` to open it, exactly as you would open a tool's body: the whole
passage, and the aria, turn and length beside the coordinate. `show` has no
gesture, so it prints the card open.

```
  ╭ ❝ lt 412.0
  │ Ecco fatto. The barber works in three strokes: isolate the environment,
  │ drive the real binary in a real pty, and assert on what the terminal kept
  │ rather than on what the renderer returned.
  ╰ 2 rows more
```

## Replying to it: the composer

In the pager, `>` opens the composer: a box that takes a PROMPT rather than
a command line, so a newline is a newline and a quote is a quote. `^S`
sends it (`M-Enter` too, where the terminal does not claim it: Windows
does); `Enter` makes a new line; `Esc` drops into its normal mode (`✎`) and
again puts the drawer away, keeping what you wrote; `^C` spends it. Tab
completes `@refs` and paths, and a paste keeps its lines (with no box open, a
paste opens the drawer by itself). The motions, the mark and the draft-local
search live in [ui-stream.md](ui-stream.md).

With a highlight up, `>` opens the composer holding `<,>`: the placeholder
expands into the coordinate at submit, so the prompt quotes the passage you
were reading. `:<,>send -- …` still works and still tokenizes like a shell
line, which is why the composer exists.

A draft ending in a `??` line is a decision rather than a prompt, and there the
quote is the focus: the window `figaro decide` sends is centred on it. See
[decisions.md](decisions.md).

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
| `:<,>comment -- <text>` | comment on the highlighted passage; on a comment, reply to it (see Comments below) |
| `:next [kind]` · `:prev [kind]` | step to the next / previous stop; `kind` is `comment`, `delta`, `fork`, `turn` or `block`. With no kind it is `^N` / `^P`. `:previous` is `:prev` |

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
| `c j` / `c k` | next / previous comment |
| `^O` / `^I` | jumplist: back / forward through the arias attended here |
| `M-m` | verbose tool output, and every node's address |
| `S` | the form in the pit |
| `D` | the last decision in the pit, again to close (see [decisions.md](decisions.md)) |

## Comments

A comment marks a passage without sending anything: highlight it, then
`:<,>comment -- why does this retry?`. Nothing reaches the model; the comment
is written to the aria's own form, and every client watching the aria draws it.

On screen the passage is washed green, and the comment hangs under the block
where the passage ends, after a blank row: `▸ ✎ gluck: why does this retry? ·
1 reply  #k7q`. Every comment has a short id (`#k7q`) and every message in its
thread an ordinal (`#k7q.2`); those are its addresses.

It is a selection stop like a form-delta row: `^N` / `^P` walk onto it, and
`c j` / `c k` jump between comments. With a comment focused its passage is
underlined as well. `Enter` walks the thread: the first opens it, the second
steps into its first message, each further one to the next, and `Enter` on the
last folds it back. An open thread's messages are stops of their own. In visual
mode, `Enter` with the cursor inside a green passage selects and opens its
comment. `y` copies the thread, or the message.

| Line | Does |
|---|---|
| `:<,>comment -- <text>` | on a highlight: a new comment. On a comment or one of its messages: a reply |
| `:<,>comment -X` | on a comment or any of its messages: delete the whole thread |
| `:comment #k7q -- <text>` | reply by id (the `#` is optional) |
| `:comment -X #k7q` | delete by id; `#k7q.2` names the same thread |
| `x` | on a selected comment or message: delete the whole thread |

With the selection on a comment, `<,>` names the comment rather than any text,
and `:` opens holding it.

From the pager a thread is append-only: a reply always goes after every
message already in it, and a delete always takes the whole thread, after a
y/n. Editing or removing one message is a board edit (`figaro set` /
`figaro unset` on `comments.<id>.thread.<key>`).

The form holds them under `comments`, keyed by id:

```
comments.<id>.at                   the coordinate token, <lt.block:a-b>!
comments.<id>.turn                 the turn the passage starts in
comments.<id>.thread.<key>.by      who wrote this entry
comments.<id>.thread.<key>.text    what they wrote
```

Thread entries sort by key. Anything that can set a key can reply or clear
one: `figaro set comments.<id>.thread.<key> '{"by":"figaro","text":"…"}'`
answers, `figaro unset comments.<id>` removes it. A comment whose passage is
not in the pager's window is reached by `c k`, which pages its turn in.

## Settings

`[quote]` in `config.toml` governs what a quote looks like in the log:

```toml
[quote]
head_chars = 480   # runes kept from the front
tail_chars = 160   # runes kept from the end
ellipsis = "…"
gutter = "> "
header = true      # the "quoting <coord>! · … · N chars" line
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
