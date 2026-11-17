{
  lib,
  pkgs,
  figaro,
  runtimeDir,
  applications,
}:
assert lib.assertMsg (
  runtimeDir != null
) "figaro role applications require an explicit runtime directory";
let
  manifest = (pkgs.formats.toml { }).generate "figaro-roles.toml" {
    roles = lib.mapAttrs (_: app: {
      id = app.roleId;
      inherit (app) outfits managed mode;
    }) applications;
  };
in
pkgs.writeShellScriptBin "figaro-apply-roles" ''
  set -euo pipefail
  for arg in "$@"; do
    case "$arg" in
      --check|-j|--json) ;;
      -h|--help)
        printf '%s\n' 'Usage: figaro-apply-roles [--check] [-j]' 'Apply declared fields to existing roles; never create, bind or start a daemon.' 'Manifest: ${manifest}'
        exit 0
        ;;
      *) printf '%s\n' 'Usage: figaro-apply-roles [--check] [-j]' >&2; exit 2 ;;
    esac
  done
  unset FIGARO_TROUPE
  export FIGARO_CONFIG_DIR=${lib.escapeShellArg (toString pkgs.emptyDirectory)}
  export FIGARO_RUNTIME_DIR=${lib.escapeShellArg runtimeDir}
  export FIGARO_NO_SELF_SPAWN=1
  exec ${lib.escapeShellArg figaro} form apply --manifest ${manifest} "$@"
''
