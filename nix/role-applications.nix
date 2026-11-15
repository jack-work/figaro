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
pkgs.writeShellScriptBin "figaro-apply-roles" ''
  set -euo pipefail
  force_check=false
  case "$#" in
    0) ;;
    1)
      case "$1" in
        --check) force_check=true ;;
        -h|--help)
          printf '%s\n' 'Usage: figaro-apply-roles [--check]' 'Apply declared fields to existing roles; never create, bind or start a daemon.'
          exit 0
          ;;
        *) printf '%s\n' 'Expected --check or --help' >&2; exit 2 ;;
      esac
      ;;
    *) printf '%s\n' 'Usage: figaro-apply-roles [--check]' >&2; exit 2 ;;
  esac
  unset FIGARO_TROUPE
  export FIGARO_CONFIG_DIR=${lib.escapeShellArg (toString pkgs.emptyDirectory)}
  export FIGARO_RUNTIME_DIR=${lib.escapeShellArg runtimeDir}
  export FIGARO_NO_SELF_SPAWN=1
  status=0
  ${lib.concatStringsSep "\n" (
    lib.mapAttrsToList (name: app: ''
      check_args=()
      if $force_check || ${if app.mode == "check" then "true" else "false"}; then
        check_args=(--check)
      fi
      printf '%s\n' ${lib.escapeShellArg "Role application: ${name}"} >&2
      if ! ${
        lib.escapeShellArgs ([
          figaro
          "form"
          "apply"
          "--id"
          app.roleId
          "-O"
          (lib.concatStringsSep "," app.outfits)
          "--managed"
          (lib.concatStringsSep "," app.managed)
          "-j"
        ])
      } "''${check_args[@]}"; then
        status=1
      fi
    '') applications
  )}
  exit "$status"
''
