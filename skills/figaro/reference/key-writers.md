# Key writers: who may write a form key

Generated from the registry in api/form/registry.go by
`go test ./api/form -run KeyWritersPage -update-key-writers`; a test fails when
this page and the registry disagree. To add a key or move it between classes,
edit its row there and regenerate. Every key a row does not cover is **self**:
any caller with write access to the form may set it, the figaro itself included.

## owner

Set or removed only by the owner: the local door, an in-process call, or the owner's principal. Every other caller, a box included, is refused. A box may still set or remove `system.retired` on the figaros inside it, so it unretires them as well as retiring them; that is intended. Removing `system.retired` unretires, and for any other caller that removal is refused like a set.

- `succession-policy` and everything under it
- `system.credo` and everything under it
- `system.provider` and everything under it
- `system.model` and everything under it
- `system.max_tokens` and everything under it
- `system.max_context_tokens` and everything under it
- `system.decide.base_url` and everything under it
- `system.tools` and everything under it
- `system.retirement` and everything under it
- `system.succession_failed` and everything under it
- `system.keep` and everything under it
- `system.ttl` and everything under it
- `system.reap_if_empty` and everything under it
- `system.keepalive` and everything under it
- `system.retired` and everything under it
- `system.methods` and everything under it
- `system.environment` and everything under it
- `system.retention` and everything under it

## lineage

Set by the owner, or by an ancestor figaro through its box door, and a figaro gives only what it holds. A figaro never sets its own.

- `target-aria` and everything under it
- `system.sandbox` and everything under it
- `system.scopes` and everything under it
- `system.grants` and everything under it
- `system.fanout.max_children` and everything under it
- `system.child_policy` and everything under it
- `system.fanout.mint` and everything under it

## runtime

Written by the daemon alone. An unprivileged patch that names one, to set or to remove it, is refused as written by the harness, not by hand. A row matches its own path only.

- `model`
- `token_budget`
- `truncation`
- `runtime` and everything under it
- `system.forked_from`
- `system.studies`
- `system.tombstone`
- `system.checkpoint.marks`
- `system.succession_warned`
- `system.failure.reason`
- `system.failure.category`
- `system.failure.turn`
- `system.failure.at`
- `system.reaped`
- `system.reaped_until`
- `system.reaped_by`

## managed apply

Managed apply refuses every `system.*` path, except these, which the owner at the terminal may name (and everything under them). A figaro caller may not; the writer rules above still judge the patch.

- `system.fanout`
