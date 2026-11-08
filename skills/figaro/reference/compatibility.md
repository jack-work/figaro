# Compatibility between a CLI and a daemon of another build

A CLI and a daemon do not have to be the same build. What decides whether
they can talk is what the daemon reports on `status` (local socket) and
`hello` (node door): its build, its release, its protocol, and a list of
capabilities.

## The contract

- **Protocol.** `rpc.ProtocolVersion` changes only when the wire breaks: a
  method renamed or removed, a field that changes type or meaning, a frame
  an older side cannot read. A different protocol is refused.
- **Patch releases** (0.52.4 to 0.52.5) never break the wire. A patch may
  add an optional field, in a request or a response, that an older side
  ignores and that changes nothing for a side that does not use it, as
  0.56.1's `except_nodes` and 0.56.2's `peers_unasked` on a listing do.
  Anything more waits for a minor.
- **Minor releases** (0.52 to 0.53) may add to it: new methods, new
  optional fields, new frames an older side can ignore. Nothing an older
  side relies on changes.
- **Anything an older side would get wrong without saying so** is a
  capability. The daemon lists it; the CLI refuses the feature by name
  against a daemon that does not. `qua.images` is the first: a daemon
  without it would run the prompt and drop the images. `form.seq` is the
  second: a daemon without it would drop a replica's client and sequence
  number, so a resent edit could apply twice; against it the form pit
  opens read-only.
- **Additive responses.** `form.access` and `form.view` mark a daemon whose
  form reads carry `access` (what this caller may write, from the write
  guard's own rules) and `view` (a table hint for a list in the form). An
  older daemon sends neither and the pit behaves as before: editable per
  form, every list nested. Neither is trusted: the daemon still refuses
  every write it would refuse without them.
- **Restoring a reaped figaro** is `figaro.reaped.restore`, a 0.54 method. It
  needs the `set` verb (not `kill`) on every node of the reaped group, and a
  troupe policy that grants `set` grants it. A set refused partway leaves the
  nodes before it restored, and the error names them.
- **@arias rows dropped `promoted` and `history_parent`** after 0.54.3,
  which shipped them. A row now carries `parent` (its history parent's id),
  and `@topology` holds the overrides: one key per figaro drawn away from its
  history, valued with the figaro it is drawn under. `@arias`'s view hint
  names `@topology` as its `overlay`; a pit from before ignores the hint and
  draws `@arias` with no override marks, and a newer pit against a 0.54.3
  daemon gets no overlay and draws that daemon's two keys as plain values.
  Writing `@topology` (a `set` on it)
  needs `set` on each figaro moved and `read` on each figaro it is drawn under.
- **`figaro.normalize` changed shape in 0.56.** The request takes
  `dry_run` (`segments` is gone); the response lists `detached` figaros and
  `dropped` overrides (`id`, `handle`, `under`, `reason`) instead of a
  `detached` count. The daemon reports `normalize.report`; a CLI refuses to
  normalize through one that does not, since an older daemon would drop
  `dry_run` and do the real thing. `figaro normalize` became `figaro
  doctor normalize`, with the old name kept hidden for one minor.
- **`figaro gc` plans before it acts.** It calls `figaro.gc.plan`,
  `figaro.gc.apply` and `figaro.gc.undo`, methods a daemon before them
  lacks; against one the verb fails with an unknown method and changes
  nothing. `figaro.gc` stays for an older CLI: it still collects childless
  outfit versions with no plan, and still runs the form-history pass.
- **`figaro.list` takes `except_nodes`** (0.56.1): nodes whose rows the
  caller already has, so the serving node does not ask them. It only
  narrows that node's fan-out to its peers; its own rows, and any node not
  named exactly by full id, are listed as before. `ls` through a troupe door
  names its local node there and adds the local daemon's rows itself; a
  node the door could not reach is reported as the door saw it, and the
  local daemon does not try it. An
  older daemon ignores the field and still asks the caller's node; the CLI
  then drops whatever that daemon says about its node, refusal included,
  in favour of its own daemon's answer. An older CLI sends nothing and
  gets today's listing.
- **A listing with no bearer asks no peer** (0.56.2): a peer would refuse
  the call anyway, so the owner's own ls lists this node only, where it
  once warned once per peer. The response's `peers_unasked` counts the
  peers it left out, and `ls` says so in one line. An older daemon still
  tries and the CLI prints its warnings as before; an older CLI ignores
  the count.
- **Folding rules.** The CLI folds form deltas with `api/form`. A change to
  how a patch folds (as in 0.52.1 and 0.52.2) bumps `FormDeltaSchema`, so
  an older mirror stops and refetches instead of folding wrongly.

## What the CLI does

| Daemon reports | CLI |
|---|---|
| the same build | accepts |
| the same protocol, the same minor release | accepts |
| the same protocol, another minor | accepts, warns once |
| another protocol | refuses, naming both releases |
| no protocol (0.52.3 and older) | the old rule: refuses any other build of the same scheme |
| a dev build on either side (no release) | the old rule |

A missing capability refuses only the feature that needs it, with the
capability's name in the error.

## Adding something to the wire

1. Additive and safe to ignore: add it. Old peers decode and ignore it.
2. Additive but wrong when ignored: add a `Capability` in `api/rpc`, list
   it in the daemon's `daemonCapabilities`, and have the CLI call
   `needCapability` before it sends the feature.
3. Anything else: bump `ProtocolVersion`, in a minor release.

`troupe.sign`'s `ttl_ms` (0.54) is case 1, both ways: a renewer from before
it ignores the request's field and grants seven days, which the asking node
keeps and renews at a third of its life; the reply's `ttl_ms` (the life
granted) is absent from an older member, and `troupe join` then says
nothing about it.

The audit behind this contract: `git diff` of `api/` and `sdk/` across
every tag since v0.51.0 found only additive changes, apart from images and
the two fold-rule fixes.

## Tapes

A tape (`listen --record`, `figaro replay`) is format 1: a header line, then
frames. The header may carry an optional `model`: the provider, name,
effective context window and max output the recording saw. A replay with it
draws that window and never asks the replaying host's `@models`; a tape
without it (older recordings) falls back to the live catalog, as before. An
older replay ignores the field. Additive, so the format stays 1.
