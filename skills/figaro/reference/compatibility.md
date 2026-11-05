# Compatibility between a CLI and a daemon of another build

A CLI and a daemon do not have to be the same build. What decides whether
they can talk is what the daemon reports on `status` (local socket) and
`hello` (node door): its build, its release, its protocol, and a list of
capabilities.

## The contract

- **Protocol.** `rpc.ProtocolVersion` changes only when the wire breaks: a
  method renamed or removed, a field that changes type or meaning, a frame
  an older side cannot read. A different protocol is refused.
- **Patch releases** (0.52.4 to 0.52.5) never change the wire.
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
