# Applying managed outfit values to an existing role

`form apply` folds named outfits on the daemon and applies only the paths named
by `--managed`. It never creates a form, binds an occupant, casts a figaro,
starts a missing daemon or changes topology.

```sh
figaro form apply --id @FULL-EXISTING-ID -O role-charter \
  --managed charter --check -j
figaro form apply --id @FULL-EXISTING-ID -O role-charter \
  --managed charter -j
```

The managed path is an example, not a mandatory role schema. Names and ID
prefixes are refused; use the exact existing role ID: `@` and 32 hexadecimal
digits, or 8 for a canonical legacy role. A bound form or
an unbound form without a holder is not an adoption target.

## Ownership and preservation

Each managed path names a deliberately owned value. Managing `charter.purpose`
preserves other charter children. Managing `charter` owns that whole object.
Duplicate and overlapping paths are refused. A child path cannot replace an
unmanaged scalar, list or null ancestor.

A managed path absent from the desired outfit removes that value. An absent
unmanaged path changes nothing. Runtime identity, holder, handoff, comments,
inference configuration and security/credential paths cannot be managed by
this operation, nor can any path that covers or enters a key the daemon holds
for a box (`succession-policy`, `worker.pool`, `target-aria` and the rest of
that list), a protected key, or the key the role's `succession-policy.handoff-key`
names. Ordinary caller read/set authorization and outfit restrictions
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
This is a per-role operation, not a transaction over multiple forms.

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

## Home Manager declarations

The Home Manager module renders `programs.figaro.outfits` into individual
`figaro/outfits/<name>.toml` files. These are public Nix store outputs: never
put credentials in an outfit.

Declare existing-role applications separately from daemon startup. Substitute
the exact existing ID and the intended local daemon's runtime directory:

```nix
programs.figaro = {
  enable = true;
  outfits.example-charter.charter = {
    purpose = "Maintain the example library.";
  };
  roleApplicationRuntimeDir = "/run/user/1000/figaro-example";
  roleApplications.example = {
    roleId = "@0123456789abcdef0123456789abcdef";
    outfits = [ "example-charter" ];
    managed = [ "charter" ];
    mode = "check";
  };
  applyRolesOnSwitch = false;
};
```

A nonempty declaration renders a TOML manifest into the store and installs
`figaro-apply-roles`, which runs `figaro form apply --manifest <file>` against
the selected runtime. Each application defaults to `mode = "check"`; changing it
to `"apply"` is explicit. Running the generated command with `--check` forces
every entry to check, including entries configured to apply, and prints the
plan; `-j` prints one JSON result per role.

The manifest is also usable by hand:

```toml
[roles.example]
id = "@0123456789abcdef0123456789abcdef"
outfits = ["example-charter"]
managed = ["charter"]
mode = "check"
```

Roles run in name order, each checked first and then written by CAS on the
version it checked. A failing role does not stop the others; the command exits
nonzero when any role failed. Unknown keys, duplicate IDs and IDs that are not
`@` and 8 or 32 hex digits are refused before the daemon is contacted. One
deadline covers the whole invocation. No declaration creates an occupant or adopts a role by display name.
The destination daemon must advertise `form.apply`.

`applyRolesOnSwitch` is off by default. If enabled, its activation step follows
outfit installation and, when `restartOnSwitch` is also enabled, the restart, forces check mode for a dry activation, and reports failures
without retrying or starting a daemon. It does not enable `restartOnSwitch`.
The generated command addresses the explicitly selected local runtime without
borrowing a configured remote endpoint, with `FIGARO_ARIA` and `FIGARO_TROUPE`
unset so the outcome does not depend on whose shell ran the switch.

Live role forms remain runtime authority for consumers. The Nix declarations
and generated command are desired values and build intermediates, not another
runtime hierarchy. No organization-specific tree or deployment policy is embedded
in this module.
