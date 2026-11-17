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
  outfits.example-charter = {
    name = "Example maintainer";
    charter = "Maintain the example library.";
  };
  roleApplicationRuntimeDir = "/run/user/1000/figaro-example";
  roleApplications.example = {
    roleId = "@0123456789abcdef0123456789abcdef";
    outfits = [ "example-charter" ];
    managed = [ "name" "charter" ];
    mode = "check";
  };
  applyRolesOnSwitch = false;
};
```

A nonempty declaration installs `figaro-apply-roles`. Each application defaults
to `mode = "check"`; changing it to `"apply"` is explicit. Running the generated
command with `--check` forces every entry to check, including entries configured
to apply. No declaration creates an occupant or adopts a role by display name.
The destination daemon must advertise `form.apply`.

`applyRolesOnSwitch` is off by default. If enabled, its activation step follows
outfit installation, forces check mode for a dry activation, and reports failures
without retrying or starting a daemon. It does not enable `restartOnSwitch`.
The generated command addresses the explicitly selected local runtime without
borrowing a configured remote endpoint; caller attribution remains intact.

Live role forms remain runtime authority for consumers. The Nix declarations
and generated command are desired values and build intermediates, not another
runtime hierarchy. No organization-specific tree or deployment policy is embedded
in this module.
