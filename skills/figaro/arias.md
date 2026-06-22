# Reading arias

An **aria** is one conversation, with a stable id and an on-disk directory
holding the canonical IR plus per-provider translator caches.

Two ways to read one — pick deliberately:

1. **The `figaro` CLI** — routes through the angelus's shared LogCache. Safe
   even while the aria is being written to live. Use for anything mid-flight.
2. **Direct JSONL reads** — `cat`/`jq`/`rg` against the files. Use for offline
   batch work across many dormant arias.

When in doubt, prefer the CLI.

## Disk layout

`~/.local/state/figaro/arias/<id>/`

```
<id>/
├── aria/<NNN>.jsonl                 IR segments (figwal NDJSON)
├── chalkboard.json                  per-aria state snapshot
├── meta.json / derived/             derived stats (msg count, tokens, last-active)
└── translations/<provider>/<NNN>.jsonl   wire cache, FK'd to the IR by FigaroLT
```

The IR is truth; translator caches are derivable, so treat them as cache.

## Entry + payload shape

Every line in any of these files is one Entry:

```json
{"LT":1,"FigaroLT":1,"Payload":{...},"Fingerprint":"","_idx":1,"_hash":"..."}
```

`Payload` is the `message.Message`. Note the key is capital `Payload`, and
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
figaro list                       enumerate arias (state, age, cwd, mantra, tokens). alias: ls
figaro show --id <id>             render a specific aria's history (last 10)
figaro show --id <id> N           last N messages
figaro show --id <id> -a          all messages, no truncation
figaro show --id <id> -v          verbose: include patches, thinking, usage, transitions
figaro show --id <id> -l          literal: raw text, no markdown (best for piping)
```

`show` needs `--id` (or a pid binding); a bare id is not positional. Thinking
blocks render muted by default. `figaro show` is the **only safe way** to read
a live aria — the angelus serves through a lock-free cache, sidestepping the
truncation race on the active segment.

## Path B — direct JSONL reads

```bash
ARIAS=~/.local/state/figaro/arias
catlog() { cat "$ARIAS/$1/aria/"*.jsonl; }   # catlog <id>
```

**List arias by message count, newest first:**

```bash
for d in $ARIAS/*/; do id=$(basename "$d")
  n=$(jq -r '.message_count // 0' "$d/meta.json" 2>/dev/null)
  printf '%s\t%s\n' "$n" "$id"
done | sort -rn | head
```

**Grep a phrase across every aria's IR:**

```bash
for d in $ARIAS/*/; do id=$(basename "$d")
  catlog "$id" | jq -r '.Payload.content[]? | .text // empty' \
    | rg -nH --color=never "phrase" | sed "s|^|$id: |"
done
```

**Every tool call in one aria:**

```bash
catlog <id> | jq -c 'select(.Payload.role=="assistant") | .Payload.content[]?
  | select(.type=="tool_invoke") | {id:.tool_call_id, name:.tool_name, args:.arguments}'
```

(Use `rg -i` for case-insensitive, `rg -F` for literal strings.)

## Picking a path

| Situation | Use |
|---|---|
| Aria might be live / actively written | CLI |
| Pretty rendering, last N messages | CLI |
| Bulk grep across every aria on disk | Direct |
| Need raw `LT`/`Fingerprint` fields | Direct |
