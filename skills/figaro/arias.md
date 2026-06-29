# Reading arias

An **aria** is one conversation. Its id is a **figwal trunk id** — stable
across forks (the continuation keeps it). The whole aria forest is backed by
**figwal** (a segmented WAL with native forking), not per-aria directories.
See [trunks.md](trunks.md) for the fork/trunk/attend model; this file is just
about *reading* an aria's contents off disk.

Two ways to read one — pick deliberately:

1. **The `figaro` CLI** — routes through the angelus's shared LogCache. Safe
   even while the aria is being written to live. Use for anything mid-flight.
2. **Direct JSONL reads** — `cat`/`jq`/`rg` against the segment files. Use for
   offline batch work across many dormant arias.

When in doubt, prefer the CLI — direct reads understand neither forks (a
trunk's history is spread across its node chain, with shared prefixes pulled
from parent dirs) nor the chalkboard watermark fold.

## Disk layout

The store root is `~/.local/state/figaro/arias/`. It is one figwal *trunk
store*, **not** a directory per aria. The aria id IS the trunk id; a trunk is
a root-to-leaf path through a fork forest of nodes, so an aria's bytes are
spread across the node dirs along its path.

```
~/.local/state/figaro/arias/
├── xwal.json                  channel manifest (main=ir, codec=jsonl, reducers)
├── policy.json                figaro side-state: the null/root trunk id + a
│                              loadout dedup map ("name@version" -> trunk id)
├── ir/                        the MAIN channel = the fork-node tree (the IR log)
│   ├── <NNN>.jsonl            root node's IR segments (figwal NDJSON)
│   ├── .trunk                 the trunk id owning this node (only in the ir tree)
│   ├── n0/                    a fork child: .fork (base index) + its own segments
│   │   ├── .fork
│   │   ├── .trunk
│   │   └── <NNN>.jsonl
│   └── n1/ …                  sibling branches; nest n0/n1/… arbitrarily deep
├── chalkboard/                reducible channel (jsonmerge): same node tree,
│   └── n0/ …                  patches on a per-segment watermark base
├── translations/<provider>/   wire cache per provider: same node tree, FK'd
│   └── n0/ …                  to the IR by mainLT (preserves thinking sigs)
├── _live/<id>.json            the single OPEN/uncommitted UI message per trunk
│                              (opaque blob, last-write-wins; discarded on restart)
├── _meta/<id>.json            derived stats (msg count, tokens, last-active)
├── _meta/<id>.<provider>.tmeta.json   per-provider translation meta
└── .daemon.lock               the exclusive store flock (one angelus per store)
```

Key points:

- **Per-channel trees, not per-aria dirs.** `ir/`, `chalkboard/`, and
  `translations/<provider>/` each mirror the same node tree (`n0/n1/…` with a
  `.fork` marker carrying the fork base index). A fork forks all channels as a
  unit. Node ids (`n<N>`) are pure plumbing — never address them; address the
  trunk (= aria) id, which lives in the `.trunk` marker in the `ir/` tree.
- **The IR is truth.** `translations/*` is a derivable wire cache; the
  chalkboard is reducible (a watermark + jsonmerge patches), so there is **no
  `chalkboard.json`** — fold it via the CLI's `state` (or figwal `StateAt`),
  don't read it raw.
- **Closed/ceremonial trunks** (the null root and loadouts) live in the same
  tree but never append — see [trunks.md](trunks.md).

## Entry + payload shape

Every line in an `ir/.../*.jsonl` segment is one figwal record:

```json
{"LT":3,"MainLT":3,"Payload":{...},"Meta":null,"_idx":3,"_hash":"..."}
```

`Payload` is the `message.Message`. Note the key is capital `Payload`;
`MainLT` is the foreign key into the IR timeline (it equals `LT` in the IR
channel itself, and points back at an IR LT in the related channels);
`_idx`/`_hash` are figwal sidecars — drop them when consuming.

```json
{
  "role": "user|assistant|tool_result|system|system.interrupt",
  "content": [
    {"type": "text", "text": "..."},
    {"type": "thinking", "text": "..."},
    {"type": "tool_invoke", "tool_call_id": "...", "tool_name": "...", "arguments": {...}},
    {"type": "tool_result", "tool_call_id": "...", "text": "...", "is_error": false},
    {"type": "interrupt", "tool_call_id": "...", "reason": "fault", "text": "..."},
    {"type": "image", "mime_type": "...", "data": "..."}
  ],
  "patches": [{"set": {...}, "remove": [...]}],
  "model": "...", "provider": "...",
  "stop_reason": "stop|tool_invoke|length|error|aborted",
  "logical_time": 0, "timestamp": 0
}
```

Assistant tool calls are `tool_invoke` (not `tool_call`). `patches` are
chalkboard mutations riding on user-role tics.

## Path A — the `figaro` CLI

```
figaro list                       the conversation forest (id, loadout, ver, fork, mantra…). alias: ls
figaro show                       render the bound aria's history (last 10 units)
figaro show <id>                  render a specific aria (the id is positional now)
figaro show <id> -n 20            last 20 units (bare-N is gone; use -n/--last)
figaro show <id> -a               every unit, no truncation
figaro show <id> -v               verbose: include patches, thinking, usage, transitions
figaro show <id> -l               literal: raw text, no markdown (best for piping)
figaro show <id> -j               units as raw JSON (materialized, no deltas)
figaro status <id> -m             provider/model/ctx + derived detail (mantra, cwd, fork origin)
figaro state <id>                 the folded chalkboard snapshot (-j for JSON)
```

`show` takes the aria id as a **positional** (or `--id`, or the pid binding);
units are labeled by their figaro LT (the coordinate `send`/`fork <id>:<LT>`
address — see [trunks.md](trunks.md)). Thinking blocks render muted by
default. `figaro show` is the **only safe way** to read a live aria — the
angelus serves through a lock-free cache, sidestepping the truncation race on
the active segment, and it stitches the trunk's node chain into one history.

## Path B — direct JSONL reads

There is **no per-aria dir** to `cat` anymore. All IR segments for all arias
live in the one `ir/` node tree, so direct reads are now *forest-wide* greps,
not per-aria ones. They cannot reconstruct a single trunk's stitched history
(that needs the fork chain) — use the CLI for that. Direct reads are good for
"does this phrase appear anywhere on disk" sweeps over dormant data.

```bash
ARIAS=~/.local/state/figaro/arias
```

**Grep a phrase across every aria's IR (forest-wide):**

```bash
rg -l --color=never -g '*.jsonl' . "$ARIAS/ir" | while read -r f; do
  jq -r '.Payload.content[]? | .text // empty' "$f" 2>/dev/null \
    | rg -nH --color=never "phrase" | sed "s|^|$f: |"
done
```

**List arias by derived message count, newest first** (from the `_meta`
sidecars, keyed by aria/trunk id — these are the reliable per-aria stat):

```bash
for m in "$ARIAS"/_meta/*.json; do
  case "$m" in *.tmeta.json) continue;; esac
  id=$(basename "$m" .json)
  n=$(jq -r '.message_count // 0' "$m" 2>/dev/null)
  printf '%s\t%s\n' "$n" "$id"
done | sort -rn | head
```

**Every tool call across the forest:**

```bash
cat "$ARIAS"/ir/**/*.jsonl | jq -c 'select(.Payload.role=="assistant") | .Payload.content[]?
  | select(.type=="tool_invoke") | {id:.tool_call_id, name:.tool_name, args:.arguments}'
```

(`**` needs globstar; use `rg --files` + a loop in a plain shell. Use `rg -i`
for case-insensitive, `rg -F` for literal strings.)

## Picking a path

| Situation | Use |
|---|---|
| Aria might be live / actively written | CLI |
| A single trunk's stitched history (across forks) | CLI |
| Pretty rendering, last N units | CLI |
| Folded chalkboard state | CLI (`state`) |
| Bulk "does X appear anywhere" grep over the forest | Direct |
| Raw `LT`/`MainLT`/figwal sidecar fields | Direct |
