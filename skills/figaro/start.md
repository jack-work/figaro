# Getting started

The first hour, in order. Read once. Everything here is a command you can run
now; nothing is theory.

## 1. You are already using it

If you are reading a figaro reply, you have already sent a prompt. The bare
form is the daily driver:

```sh
figaro -- say buongiorno back to me
```

The `--` is mandatory and everything after it is the prompt. That is the whole
gesture. There is no session to start and no file to create.

## 2. Turn on completion

Do this second, because it makes everything after it easier. Aria ids are
opaque hex, and completion means you never type one by hand.

```sh
figaro completion install
```

Open a new shell afterwards. Now `figaro attend <TAB>` offers real arias.

## 3. Arias, and walking among them

An **aria** is one conversation. It has an id, a mantra (a short phrase naming
what it is about), and a history.

```sh
figaro ls                    what conversations exist, scoped to where you are
figaro show <id> -n 5        the last five turns of one
figaro attend <id>           bind this shell to it, like cd
figaro -- <prompt>           now this prompts that aria
figaro attend null           go home, unbind
```

`attend` is the `cd` of the system and `ls` follows it: attended, `ls` shows
your own lineage; unattended, it shows every top-level aria. There is no
`detach`; `attend null` is how you leave.

Two flags on `send` are worth learning on day one:

```sh
figaro send -f -- <prompt>   fire and forget: submit and get your shell back
figaro send -er -- <prompt>  a throwaway aria, plain text out, good for pipes
```

Without `-f`, `send` stays attached and streams until the turn ends. Ctrl-D
detaches without killing the turn, Ctrl-C interrupts it.

## 4. Forking, which is the point

A conversation is not a line, it is a tree. When a conversation has gone
somewhere useful and you want to try a different direction without losing it,
branch it.

```sh
figaro fork                    branch where you are, at the head
figaro fork -- <prompt>        branch and speak on the branch
figaro fork <id>:12 -- <p>     branch at turn 12, as if that turn went differently
```

Turn numbers are the ones `figaro show` prints. The original is never
rewritten. The model behind this, including which child keeps the id, is
[reference/trunks.md](reference/trunks.md).

## 5. The chalkboard, and the mantra

Every aria carries a small key-value slate that rides along with the
conversation. The agent sees non-`system` keys change as they change.

```sh
figaro state                                    read the whole slate
figaro set mantra "learning figaro"             set one key, no model round trip
```

The **mantra** is the phrase shown in `figaro ls`, so a well-kept mantra is how
a list of twenty conversations stays readable. Agents maintain their own; see
[reference/mantra.md](reference/mantra.md).

## 6. The outfit, which makes it yours

An **outfit** is a named patch for the chalkboard: which model, which credo
(the standing instructions that shape voice and behaviour), and which skills
are available. It lives in `~/.config/figaro/outfits/<name>.toml`, and
`config.toml` names the default.

```sh
figaro state outfit --list        what profiles exist
figaro new -O <name>              start a conversation under one
figaro send -O <name> -- <p>      dress the aria you are talking to, then ask
figaro state outfit <name>        dress it now, with nothing to say
figaro state outfit --tree <name> draw the layer closure; applies nothing
```

Outfits compose. An outfit may declare `layers`, and several may be named at
once (`-O a,b`, folded left to right). Applying one is additive: keys already
holding that value are skipped, nothing is ever removed, and the agent sees a
`<system-reminder>` for exactly what changed. The whole model, including the
inline `-O ttl=1h` form and what "birth" means differently from "fold", is
[reference/outfits.md](reference/outfits.md).

Skills are markdown files under `~/.config/figaro/skills/`. Each one's
front-matter description is always visible to the agent; the body is read only
when the agent decides the skill applies. That is why a good description says
*when* to read a file rather than what is in it.

First-party skills ship inside the binary and are overridden by name from your
config directory. If you shadow one, you own the copy, and it will drift.

## Where to go next

| You want to | Read |
|---|---|
| A command that is not above | [cli.md](cli.md) |
| To script figaro, or drive it from an agent | [agents.md](agents.md) |
| To change figaro itself | [maintaining.md](maintaining.md) |
