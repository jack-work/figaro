# Staged example: dropping `mint` from the default outfit's scopes

Not applied to any outfit or config in this repo or on disk. `figaro.create`
(`figaro new`) now needs the `mint` verb; `form.create`/`cast`-minted roles
stay on `create`. This is what the eventual Home Manager / outfit change
looks like once central command computes real per-role limits (fanout-limits
proposal, step 3):

```toml
# the default outfit: narrowed, no root minting, fan-out off unless granted
[system]
scopes = ["read:*", "listen:*", "send:*", "fork:*", "set:*", "create:*", "kill:aria:self"]

# the King's outfit: keeps both
[system]
scopes = ["*:*"]
```

A figaro under the narrowed default can still `cast` itself into a role,
`import`, and fork (subject to `system.fanout.max_children`), but `figaro
new` is refused: no verb named `mint` in its scopes. The King's `*:*` is
unaffected by this split; it already covers every verb, named or not yet
invented.

Scopes only narrow down the lineage (an ancestor may grant at most what it
holds), so no fork of an ordinary figaro gets `mint` back by surprise.
Gluck at a terminal carries no figaro label and is unaffected either way.
