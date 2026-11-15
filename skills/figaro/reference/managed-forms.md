# Applying managed outfit values to an existing role

`form apply` folds named outfits on the daemon and applies only the paths named
by `--managed`. It never creates a form, binds an occupant, casts a figaro,
starts a missing daemon or changes topology.

```sh
figaro form apply --id @FULL-EXISTING-ID -O role-charter \
  --managed name,charter,supervisor,projects --check -j
figaro form apply --id @FULL-EXISTING-ID -O role-charter \
  --managed name,charter,supervisor,projects -j
```

The paths above are an example, not a mandatory role schema. Names and ID
prefixes are refused; use the exact existing role ID, including canonical
legacy IDs that are shorter than 32 hexadecimal characters. A bound form or
an unbound form without a holder is not an adoption target.

## Ownership and preservation

Each managed path names a deliberately owned value. Managing `charter.purpose`
preserves other charter children. Managing `charter` owns that whole object.
Duplicate and overlapping paths are refused. A child path cannot replace an
unmanaged scalar, list or null ancestor.

A managed path absent from the desired outfit removes that value. An absent
unmanaged path changes nothing. Runtime identity, holder, handoff, comments,
inference configuration and security/credential paths cannot be managed by
this operation. Ordinary caller read/set authorization and outfit restrictions
still apply; `--managed` is not an authorization grant.

## Check, apply and uncertain outcomes

Check returns the role version, an identity for the managed outfit values,
planned set/removal paths, and `would-apply` or `unchanged`. It writes nothing.
The report does not include field values.

Apply checks once, then submits the patch with that version and outfit
identity. The daemon checks both again and uses the normal durable form
writer. Concurrent role changes or changed managed outfit values refuse the
write. A repeat against unchanged values reports `unchanged` without moving
the durable version. An empty patch still checks the expected version.

There is no automatic retry. A timeout or disconnect after submission can
mean an unknown outcome: read/check again before deciding what remains.
This is a per-role operation, not a transaction over a formation.

## Wire and compatibility

The daemon advertises `form.apply`; the CLI refuses an older daemon rather
than falling back to shell patching or read-then-create.

`form.apply` takes `figaro_id`, `outfits`, `managed`, `check`, `if_version` and
`expected_outfit`. Non-check calls require a nonzero expected role version and
the managed outfit identity from check. There is no optimistic `client`/`seq`
queue on this operation. Success reports the target, resulting version,
outfit identity, outcome and changed paths.

A host flake may render outfits and application arguments as immutable build
outputs. The applied role forms remain runtime authority. This command does
not introduce a parallel role registry, a reconciler service or automatic
missing-role provisioning. Startup ownership and operator-approved activation
belong to the host configuration.
