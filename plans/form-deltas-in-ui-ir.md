# Form deltas in the UI IR

Plan of record for the work Gluck specified on 2026-08-13. Written by aria
b2b0c543 (session 4) for the role holder who takes it; I am not that holder,
so this is a plan and not a claim about built code.

---

## 0. What this is

A reader of a transcript cannot see WHY a turn did what it did, because the
state the model was shown is invisible: the bound board's keys, a studied
form's transitions, the moment a role was recast. The provider sees all of
it as system-reminders; the client sees none of it.

So: **every UI IR node gains an optional `formDeltas` property**, the client
draws it dimly, and two of those deltas get sentences of their own.

## 1. The shape

On `livedoc.Node` and on the turn's inquiry (`aria.Turn`), optional and
omitted when empty:

```go
// FormDeltas is the form state a reader would have needed to understand
// this node: what changed, on which form, and what kind of form it was.
// Keyed "<formid>.<path>" so a client can group by form id without
// parsing a nested shape, and sort stably.
FormDeltas map[string]FormDelta `json:"formDeltas,omitempty"`

type FormDelta struct {
    Value json.RawMessage `json:"value,omitempty"` // as seen HERE; absent on removal
    Kind  FormKind        `json:"kind"`            // bound | studied | role
    Event FormEvent       `json:"event,omitempty"` // set | removed | deleted
    Form  string          `json:"form"`            // the id, unsplit, for grouping
}
```

`Kind` is `bound`, `studied`, or `role` — a form carrying `target-aria` is a
role, and that is decided server-side so three clients do not each invent the
predicate. `Event` distinguishes a key REMOVED from the whole form DELETED
(`system.libretto.alive: false` on the copy), because a reader needs to know
the difference between "the brief was cleared" and "the brief is gone".

**Tolerate it on every node type.** Gluck asked whether a fork at a thinking
node would need it; the answer is to give it to all of them rather than
maintain a list of which nodes may carry state. It is `omitempty`; a node
without deltas costs one absent field.

## 2. Where it is assembled — NOT from the provider cache

**The hub assembles this from the store, by the same cursor arithmetic the
projection does, and never from the provider's translated bytes.** This is
the load-bearing constraint and the reason to write it down:

- the provider cache holds RENDERED bytes for one dialect, per LT, keyed by a
  fingerprint. Deriving UI state from it makes the UI a function of which
  provider last spoke, and of whether a record happened to be cached.
- it is also the layer that has twice made a wrong rendering permanent.

The mechanism to copy (not to call) is `internal/provider/projection.go`:

1. each IR record carries cursors — `libretto:<sourceid>` for observed forms,
   the node's own form channel for the bound board;
2. consecutive records bracket a window;
3. the patches in that window come from `Libretto(source).PatchesBetween` for
   a studied form, and from the board's own `FormPatchesBetween` for the
   bound one.

Three rules inherited from the projection, each of which was a bug first:

- **read a libretto through `Libretto(source)`**, never as a node. A second
  `Form` over that channel replays once and never hears the fold again.
- **strip `system.libretto.*` except `alive`** (`store.HiddenLibrettoKey`).
  `at` moves on every fold; `refs` moves when ANOTHER aria studies the same
  form. Neither is this reader's business.
- **a window may only close on a record that can carry it.** For the model
  that meant `RoleInput`; for the UI IR the analogous question is which node
  a window belongs to. Pick the rule deliberately and write it in the code:
  the natural one is that a window closes on the node projected from the
  record that carries the stamp, which for a tool round is the tool node.

Legacy `study:` stamps (source versions, pre-migration) are IGNORED, exactly
as the translator ignores them. An old transcript shows no form deltas.

## 3. The two sentences

Both are derived from ordinary deltas; neither needs a new channel.

| when | the TUI says |
|---|---|
| `aria_id` changes on the BOUND form | *This figaro has been forked from `<id>`* |
| `target-aria` changes on a form of kind `role` | *Role `<formid>` recast to figaro `<aria id>`* |

The first is available today without inference: a fork's birth patch stamps
`system.forked_from` beside the new `aria_id`, so the sentence can name the
parent from the delta itself rather than reconstructing it.

## 4. The client

Dimmer than the selection UI, slightly opaque, Kanagawa. Concretely:

- one line per form, grouped by form id, keys joined — not one line per key,
  or a board write of six keys becomes six lines of furniture.
- rendered BELOW the node it belongs to, indented, in the theme's muted
  foreground; the selection UI keeps its brighter treatment, so the eye
  ranks selection above state and state above nothing.
- the two sentences above render as prose, not as a key/value pair.
- collapsed by default if a form delta exceeds a couple of lines: studied
  values are mirrored WHOLE and are not truncated anywhere in the pipeline.

Global theming: take colours from the theme table rather than literals, and
add a token (`stateDim` or similar) rather than reusing the comment colour by
coincidence. One name, one meaning.

## 5. Build order

1. **The types** (`livedoc.Node`, `aria.Turn`, `FormDelta`): additive,
   `omitempty`, nothing reads them yet. Wire-compatible with old clients.
2. **The hub-side assembly**, behind a function with its own test: given a
   record range and a store, produce the deltas. This is where the cursor
   arithmetic lives and where the three inherited rules are enforced.
3. **The projector** (`internal/uiir`) attaches them to nodes.
4. **The TUI** draws them.
5. **The two sentences**, last, because they are a rendering of (1)-(4) and
   not a mechanism.

Each step is independently testable, and 1-2 are safe to land alone.

## 6. What will bite

- **Cost per turn.** This adds a store read per stamped record on a path that
  runs for every rendered turn. The projection pays it already and has a
  per-LT cache; the UI IR has no such cache. Measure before shipping: the
  `DaemonDay` and `ListingCost` probes exist for exactly this.
- **A form deleted mid-transcript.** The copy outlives it, so the delta still
  renders; the source read would fail. Another reason to read the libretto.
- **Ephemeral arias** have no store and no librettos. The field is simply
  absent; do not synthesize it from the provider path to fill the gap.
- **Retranslation must not change it.** These deltas are derived from durable
  stamps, so two renders of the same transcript must agree. That is a test:
  render twice, assert equality, and make it fail if anyone reaches for the
  provider cache to speed it up.

---

## 7. The fork-on-a-form bug, confirmed (same session, filed here for the same hand)

`fig fork @form` births a **figaro**, not a forked form: verified live, the
child is `kind=conversation` with `aria_id` and `system.forked_from` on its
board. So `fork` and `bind` on a form are SYNONYMS and one of the two names
is a lie.

Cause, one line: `XwalStore.ForkWith` hard-codes
`forkWithKind(..., kindConversation)`, while `forkWithKind`'s own comment
says the caller chooses the child's species. The fork verb has no way to say.

**The form→form operation already exists and is already documented**:
`CreateForm(parent, patch)` — *"Parent \"\" forks the null root; a form id
duplicates that form's state into a fresh @id"* (`rpc.FormCreateRequest`).
`createFormAndReport` even takes a `parent`. **Every CLI caller passes ""**,
so nothing reaches it. `fig form new` while attended to a form mints a
SIBLING off the null root and inherits nothing — verified.

So the fix is wiring, not mechanism: `fig fork` on a node of kind `form`
routes to `CreateForm(parent)`. Two real questions with it:

1. a form's timeline is one ceremonial record, so `<id>:<turn>` addresses
   nothing — a form fork is always at the head, and the verb should say so
   rather than silently ignoring a coordinate.
2. `CreateForm` REQUIRES a patch (*"a fork that transforms nothing is a fork
   nobody can name"*) and `fig fork` takes none. Either fork-a-form demands
   `-S`, or that rule is relaxed for this path. That is a ruling, not an
   implementation detail.

Recorded and NOT to be built (Gluck, explicitly): `cast` on a form with no
argument minting a figaro, and a role fork meaning fork-both-and-recast.
