# 2026-09-16: the attendance ring, and whether a client's navigation lives in a form

**Status:** brainstorm. Nothing built. Gluck stopped mid-thought ("I am
realizing I need to think through this idea some more") and asked for the
thinking to be captured before it evaporated. Scribe: aria `90ec6584`.
Structural edits are marked; the reasoning is his, in his framing.

## What prompted it

Bugs while working the fork stack. Two seen live in this session:

- a fork's delta row did not appear in the child's transcript as soon as the
  child existed, so there was nothing on screen to navigate from. "Figaro
  should have seen the fork delta immediately upon being forked. I need the
  delta in the transcript so navigation between arias is possible."
- `^O` would not go back to the aria the reader came from.

The astra review (`/var/tmp/23e03e27/review.md`, finding 6) reports the same
thing from the source side: `a` records a departure, `:attend` records only
an arrival, `:fork` retargets and bypasses the history entirely, startup
seeds nothing, and a failed hop still moves the cursor. Gluck's read is that
this is not loose wiring: "I think the architecture is incorrect."

## Gluck's framing, preserved

### The structure is a ring, not a list

> The fork stack should be a simple ring buffer that pushes (writes under
> cursor) or pops (decrements cursor) the navigated tab if the path to entry
> is any way besides the forward and back controls.

So: one cursor over a fixed-length buffer. Arriving anywhere by a route that
is not the back/forward controls writes at the cursor. Back and forward only
move the cursor. Entering from the middle therefore truncates the future,
which is the browser rule the current list already tries to keep.

> The client should just not allow walking further back in the history than
> is stored, we should not walk into the newest history item.

A full ring must refuse to wrap past its oldest entry into the newest one.

### What the TUI shows

> The length of the stack should not be exposed for the time being.

> Forward and backward should navigate to the previous location and display a
> notification in the status bar, but should not show the capacity.

> The implementation details for the ring buffer should be largely hidden on
> the tui. The ring buffer should really be an implementation detail, it just
> makes managing the form possible, otherwise we have awkward lists.

And it is not only a pager gesture:

> There should also be a cli action for forward and backwards.

### Configuration

> The ring buffer length should be configurable in the config.toml, cleanly
> separated, by file if a separate one already exists, separate from the
> backend configuration, since this should be purely an in-memory client tui
> construct.

> When client cli config refreshes, the ring buffers need to be normalized to
> avoid badly managed memory.

He also wants the update path itself to exist and to match the daemon's:

> We should have a cli config update thing like for outfits, or however the
> server/daemon takes config updates. Make the cli congruent with the server,
> and add the server path if it doesnt already exist.

### The larger idea: the buffer as a form

> Though I should note that it would be nice to have this in the cli as
> well..... hmm..... perhaps we can do something similar using figaro? We
> should pay some thought to what form that might take, if we literally allow
> the client to represent forward/back buffers in a literal figaro form on the
> daemon.

> What do we think about putting the clients attend buffer into a form in the
> figaro? It would define the currently bound figaro for any given process,
> and it could be resolved automatically in the middleware by a client via
> some descriptive id, in this case a pid.

The mechanism he names for it is the intrinsic syntax:

> The server should instead expose the capability to create in memory forms on
> the fly. the /syntax we recently defined for /runtime. but the client should
> create and manage it and should handle config updates for it.

Call it `/attendance`.

> This /attendance form should be the canonical current location. angelus bind
> should be to create it. cli should be refactored to make one call to the
> angelus daemon and provide some id argument that qualifies its process as
> its id. angelus should support a bind operation, and then the rest of the
> calls can be made using that processs pid as its id and it should be routed
> to the attended aria according to the current item of the ring buffer.

### The distribution tension

> That would mean that the figaro client really must have a figaro server,
> which hurts the possibility to distribute the cli tui separately without
> also shipping the daemon, or taking a network dependency on something that
> could derive from a client-local cache potentially.

### Federation, as the way out of that tension

> I suppose the local figaro could always be federated in a way that purely
> reflects upstream changes for readonly baselines for fast loading if the
> upstream servers are busy. That might actually make a lot of sense. We
> already have a pretty good cache representation in figaro for UI data.
> Streaming over a unix socket is fast.

### Identity: what a session is called

> The browser clients would eschew the same type of binding entirely and
> probably manage state internally, though I admit it would be nice to bind
> the identity of the UI element to something as stable as process id has
> proven when developing figaro... nevertheless I have not had issues in the
> tui either, so maybe that is a wash.

> We will just trust the pids binding, and I suppose to account for when we
> federate we can make a comment about how this would also have to include
> some kind of qualifying information like the hostname or something since
> pids obviously arent globally unique, but something to uniquely identify the
> cli session AND the current UI window.

Two axes, then: which session, and which window inside it.

### Persistence

> Fig listen can have persistent forward and backs. Although perhaps not
> persistent. Since processes are not persistent. So maybe they can just be in
> memory only, and written to disk only on fig stop -k, for --keep or
> whatever. That has worked for us so far. The forms for the ring buffers
> would be small, and we can rehydrate them easily enough on load. That part
> of the code is old, so modernize where possible.

### Ownership: the daemon is blind

> Even though this ring buffer is persisted on the daemon side, however, it is
> owned and managed by the cli client in this case. The daemon is blind. The
> daemon will pair a cli pid to an aria and it will track a ring buffer for
> attendance stack, but it wont know what the attendance stack is. Be careful
> to reference it on the daemon end, a deference is appropriate, but only
> once.

### The folly, and the redundant target-aria

He caught the contradiction himself, and it is the crux of the design:

> Now I realize I have follied in my reasoning. The cli cannot manage the ring
> buffer, a complex data structure, but expect the server to know how to
> interpret what the attended aria is. Hmm maybe a way around this is to
> include a redundant target-aria attribute on the runtime /attendance from
> which by convention always duplicates the current item on the /attendance
> stack/ring buffer.

> The current target aria should always be updated atomically with the ring
> buffer position, and furthermore, the currently bound ui window should be
> derived from the target aria value.

The daemon reads exactly one key. The ring is opaque payload beside it. The
atomicity of "cursor moved and target-aria moved" is the whole contract, and
a form patch is already atomic, which is why the structure can live there at
all.

Note that `target-aria` is the same duck type a role already uses: an unbound
form carrying `target-aria` is a role, and `fig send @role` reaches whoever
holds it. An `/attendance` form carrying `target-aria` would read as a role
whose target is wherever the reader currently stands. That may be a happy
accident or a collision; unexamined so far. (Scribe's observation, flagged
here because it bears on the key name.)

### Why a ring at all

> The ring buffer I think is good because it is an efficient way to represent
> a fixed length data structure, and ours is good at maps and bad at growing
> lists. and arrays are maps basically. and buffers are arrays. I think you
> can figure out how to implement a stack out of a ring buffer, but ill take
> your lead if im looking at this naively.

### Where he stopped

> So when the ui bootstraps in transcript mode (or incipit mode), it should
> subscribe to whatever aria ..... okay I am realizing i need to think through
> this idea some more.

## What exists today

Checked against source at `e21408e0` (branch `fix/quote-review`), so a later
reader knows what was true when this was written.

- **The jumplist is a growing slice in one session's memory.**
  `internal/cli/forkjump.go:207` holds `ariaJumplist{ids []string; pos int}`,
  with `visit` truncating the future and `hop` refusing at either end. It
  lives on `interactiveInput.jumps` (`internal/cli/stream.go:122`) and dies
  with the process. No cap, no config, and `where()` returns "2/5" which
  `internal/cli/command.go:342` prints: the capacity Gluck wants hidden.
- **Attendance is a pid binding, not a form.** Invariant 6 in `agents.md`:
  "PID binding is 1:1. Shell PID to at most one figaro. `pid.bind` /
  `pid.unbind` / `pid.resolve` go through the angelus." The binding is
  registry state (`internal/angelus/registry.go`), not a patchable document.
- **Bindings already persist on demand.** `saveBindings`
  (`internal/angelus/admin_handlers.go:209`) writes the registry to
  `BindingsPath()`, and the CLI calls it on the way down. The comment above
  that call, `internal/cli/system.go:30`, reads "TODO: server should save
  bindings automatically". This is the same in-memory-then-written-at-stop
  shape Gluck proposes for the ring.
- **Intrinsics are the existing mechanism for a live, unwritten form.** The
  address grammar is `<host>/<intrinsic>`, documented in
  `skills/figaro/reference/forms.md:250`, with `<id>/state`, `<id>/runtime`
  and `<id>/queue` today. An intrinsic "is not mintable, forkable, bindable,
  or listed by `form ls`. It is a projection its host publishes, and it dies
  with its host." Note the direction: today a host publishes an intrinsic, and
  `/attendance` inverts that, since the client would be the writer and the
  daemon merely the place it lives. That inversion is the new capability he is
  asking for ("expose the capability to create in memory forms on the fly").
- **Clients already mirror intrinsics live.** `internal/cli/intrinsic_mirror.go`
  keeps `runtime` and `queue` copies current through the patch protocol and
  fences them by subject generation. A `/attendance` mirror would ride the
  same machinery.
- **Config has a client section but no update path.** `internal/config/config.go`
  carries a `CLIConfig` under the toml key `cli`, and a top level `QuoteConfig`
  under `quote`. Nothing on the CLI side takes a live config update the way
  outfits are applied; that path is the one he says to add.

## Open questions

His, restated as questions rather than answered:

1. What identifies a session: pid alone today, pid plus host under federation,
   and something further for "which UI window". What is that third thing, and
   who mints it?
2. Where does the ring live when there is no daemon to hold it, if the TUI is
   ever distributed alone? Federated read-only mirror, or a local file?
3. Persistent or not: rehydrate the ring at startup, or start empty every
   process and only write on `stop --keep`?
4. Does the daemon's one dereference of `target-aria` mean routing (angelus
   resolves a call to the current aria) or only reporting?
5. Is `target-aria` the right key name given roles already own that duck type?
6. What normalizes a ring when the configured length changes under it: clamp
   to newest N, or reset?

Scribe's additions, clearly separable:

7. The bug that started this is upstream of all of it. A child's fork delta
   must be in its own transcript at birth, or there is nothing to navigate
   between. Today `formdelta.Attach` defers a trailing seam record to "the
   turn it opens", and at birth that turn does not exist yet, so the banner
   waits. Whatever the ring becomes, that stays a separate fix.
8. Recording a departure and an arrival through one operation (review finding
   6) is a prerequisite: a ring with three entry points that each write it
   differently has the same bug in a new shape.
