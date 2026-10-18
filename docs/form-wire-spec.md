# The form wire, exactly

Every frame an application can send to, or hear from, ONE form. Not the
queue — an ordinary unbound form (`@id`).

Every payload below is a **verbatim capture** from `cmd/form-test` against a
live daemon on 2026-09-09, not a reconstruction. The form is `@1f84ca51`.

---

## 0. The shape of the thing

```
          ┌── angelus.sock, every call carries figaro_id ───────────┐
client ───┤  figaro.follow (req/resp)   follow without asking       │
          │  figaro.form   (req/resp)   read the snapshot           │
          │  figaro.set    (req/resp)   write a patch               │
          │  form.delta    (push)       every committed patch       │
          └─────────────────────────────────────────────────────────┘
```

**Framing:** newline-delimited JSON over a unix socket. One JSON object per
line, both directions (`jkrpc` uses `json.Encoder`/`Decoder`). No
Content-Length, no batching.

**Three rules that are not guessable:**

1. **Follow explicitly.** `figaro.follow` makes the connection a follower of
   `@1f84ca51`, and `ariaHub.Notify` fans to every follower. Reads and sets
   never subscribe. Every push carries `figaro_id`.
2. **Notifications interleave with responses, and the push usually wins.**
   Measured: `figaro.set` on a connection returns `form.delta` for your own
   write *before* the reply to it. **Match on `id`.** A client that reads one
   line and calls it the answer reads the wrong frame.
3. **A form has no agent.** All three verbs are served by the hub from the
   store (`writeForHub`, `readFromStore`). Nothing is woken; nothing can be
   busy. This is why a form is the clean specimen.

---

## 1. `figaro.follow`: start hearing deltas

```json
→ {"jsonrpc":"2.0","id":1,"method":"figaro.follow","params":{"figaro_id":"@1f84ca51"}}
← {"jsonrpc":"2.0","id":1,"result":{"ok":true}}
```

An unknown id is refused here. The door is `angelus.sock` under
`FIGARO_RUNTIME_DIR` (else `$XDG_RUNTIME_DIR/figaro`).

---

## 2. `figaro.form` — read the snapshot

```json
→ {"id":2,"jsonrpc":"2.0","method":"figaro.form","params":{"figaro_id":"@1f84ca51"}}
← {"id":2,"jsonrpc":"2.0","result":{"snapshot":{"title":"demo"},"version":2}}
```

`snapshot` is the board as **nested JSON**, not flat dotted keys. `version` is
the durable version, and it is the number every delta is sequenced against.

**Seed first, then follow.** Subscribing after reading drops whatever landed
in between. Subscribing first (`figaro.follow`, per rule 1)
and reading second means the seed may be *older* than a delta already in hand
— which the version check below catches. That is the correct order and it is
what `openFormView` does (`internal/cli/form_listen.go:33`).

---

## 3. `figaro.set` — write a patch

### Request

```json
→ {"id":1,"jsonrpc":"2.0","method":"figaro.set","params":{
     "patch":{"object":{"Set":{"count":7,"tags":["a","b"],"title":"rewritten"}}}
   }}
```

Full `params`:

| field | type | meaning |
|---|---|---|
| `patch` | `Patch` | required; §5 |
| `outfits` | `[]string` | outfit NAMES, folded **under** the patch's own keys |
| `if_version` | `uint64` | refuse unless the board is still at this version; 0 = unconditional |
| `assert` | `bool` | make a removal of an absent key a **refusal** rather than a no-op |
| `wait` | `bool` | ask a live aria for the writer's verdict instead of `queued` (moot for a form: no agent, always synchronous) |

### Response

```json
← {"id":1,"jsonrpc":"2.0","result":{
     "ok":true,"outcome":"applied","set":["note"],"version":4}}
```

`set` and `remove` are **what landed**, not what was asked for.

### The identity rule — measured

Setting a key to the value it already holds:

```json
→ {"...":"figaro.set","params":{"patch":{"object":{"Set":{"note":"hello"}}}}}
← {"id":1,"jsonrpc":"2.0","result":{"ok":true,"outcome":"unchanged","version":4}}
```

`outcome: "unchanged"`, **the version does not advance, and NO `form.delta` is
emitted.** The writer diffs (`snap.Apply(p).Diff(snap)`) and drops an identity.
A client may therefore republish state unconditionally and pay nothing —
which is the property that makes a form a good push channel.

### Refusals — measured

```json
→ …"patch":{"object":{"Delete":{"nope":null}}},"assert":true
← {"id":1,"jsonrpc":"2.0","error":{"code":-32000,"message":"remove \"nope\": no such key"}}

→ …"patch":{"object":{"Set":{"x":1}}},"if_version":2
← {"id":1,"jsonrpc":"2.0","error":{"code":-32000,
     "message":"form moved: at version 5, not 2: re-read and retry"}}
```

Refusals are JSON-RPC **errors** (`-32000`), not outcomes. Contrast the queue,
where per-id refusals are results — because there a single request carries
many ids and a notification cannot carry a per-request outcome.

---

## 4. `form.delta` — the push

Unasked, to every attached connection, once per committed patch.

```json
← {"jsonrpc":"2.0","method":"form.delta","params":{
     "schema":1,
     "aria_id":"@1f84ca51",
     "version":3,
     "patch":{"object":{
       "Set":{"count":7,"tags":["a","b"]},
       "Update":{"title":{"scalar":{"Before":"demo","After":"rewritten"}}}}},
     "at":1789006055414
   }}
```

| field | meaning |
|---|---|
| `schema` | envelope version, currently `1`. A frame you cannot read → **stop tracking**; there is nothing to re-read. |
| `aria_id` | the form; carried so one client can multiplex several |
| `version` | the durable version **after** this patch |
| `patch` | §5 — the same type a client SENDS, deliberately |
| `at` | unix millis, server clock |

**Note in the capture above:** one `figaro.set` produced **one** delta
containing both a `Set` (new keys) and an `Update` (an existing key). The
writer classifies per key; a new key is `Set`, an existing key is `Update`
carrying a `scalar` with `Before`. That distinction is free undo.

### The client's version rule

Exactly three outcomes (`internal/cli/form_mirror.go:52`):

```
d.Schema != 1          → incompatible. Stop. No resync will help.
d.Version <= mine      → already held. Drop it. (A replay after a resync is
                         not a gap.)
d.Version != mine+1    → GAP. Re-read figaro.form and reset the mirror.
otherwise              → apply; mine = d.Version
```

There is no ack, no replay request, no nack. **Resync is by re-reading the
snapshot**, and that is the whole recovery story.

---

## 5. `Patch` — the payload proper

`api/form/patch.go`. **A patch describes its own shape**, so a reader
dispatches on which field is non-nil rather than consulting a schema. Exactly
one of three is set; the zero patch is identity.

```jsonc
{"scalar": {"Before": <v>, "After": <v>}}         // a whole-value swap

{"object": {                                       // the keys of an object
   "Set":    {"k": <v>},                           //   created
   "Delete": {"k": <v>},                           //   removed (carries the OLD value)
   "Update": {"k": <Patch>},                       //   changed, recursively
   "New":    {"k": true}                           //   which Updates did not exist
}}

{"list": {                                         // a keyed, ordered collection
   "Create": [{"Key":"k","Value":<v>}],
   "Delete": [{"Key":"k","Value":<v>}],
   "Update": {"k": <Patch>},
   "Order":  {"Declared":["a","b"],"Moved":[1],"Prior":[…],"PriorMoved":[…]}
}}
```

Two properties worth stating out loud, because they are what the design buys:

- **Every operation carries the value it displaces.** `Delete` holds the old
  value; `scalar` holds `Before`. So **`Inverse` is a swap** — undo is
  structural, not a journal.
- **`New` exists so patches compose.** Two patches adding different fields
  under one parent describe their leaves rather than the subtree; `New` is
  what lets `Inverse` remove the parent instead of leaving it empty.

`Order` is a *partial window*: the keys that moved, plus one neighbour either
side as an anchor. Undeclared keys keep their position. `Prior` is the same
window in source order, again so inverse is a swap.

### Writing one by hand

Set, from the capture: `{"object":{"Set":{"note":"hello"}}}`

Delete: `{"object":{"Delete":{"note":null}}}` — the value you supply is
ignored on the way in; the server fills the real old value into the delta it
emits (`"Delete":{"note":"hello"}` came back).

---

## 6. What ELSE can arrive on this socket

For a **form**, nothing. `figaro.aria` and `turn.done` are the other two
pushes (`api/rpc/methods.go`), and both require an agent, which a form does
not have and cannot get. A form socket is a single-channel stream.

That is precisely why it is the right specimen for this exercise — and why the
same machinery is worth pointing at things that *do* move (see
`history:plans/reactive-queue-and-state-forms.md`).

---

## 8. Open, for the author

1. The demo form `@1f84ca51` is in your **real** store (my isolation attempt
   fell through — `FIGARO_RUNTIME_DIR`, not `FIGARO_STATE_DIR`, is the knob).
   Say the word and I'll `figaro kill @1f84ca51`.
