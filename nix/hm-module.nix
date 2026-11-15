{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.figaro;
  inherit (lib) mkOption types;
  toml = pkgs.formats.toml { };
  figaro = "${cfg.package}/bin/figaro";
  restart = "${figaro} upgrade --to ${figaro} --if-changed --wait ${toString cfg.restartWait}";
  applicationType = types.submodule {
    options = {
      roleId = mkOption {
        type = types.strMatching "@[0-9a-f]+";
        description = "Exact existing role ID, not a name or prefix.";
      };
      outfits = mkOption {
        type = types.listOf types.str;
        description = "Installed daemon outfit names, folded in order.";
      };
      managed = mkOption {
        type = types.listOf types.str;
        description = "Explicit paths whose values this declaration manages.";
      };
      mode = mkOption {
        type = types.enum [
          "check"
          "apply"
        ];
        default = "check";
        description = "Check by default; applying changes requires an explicit selection.";
      };
    };
  };
  roleApplications = import ./role-applications.nix {
    inherit lib pkgs;
    runtimeDir = cfg.roleApplicationRuntimeDir;
    applications = cfg.roleApplications;
    inherit figaro;
  };
in
{
  options.programs.figaro = {
    enable = lib.mkEnableOption "figaro";

    package = mkOption {
      type = types.package;
      description = "The figaro package.";
    };

    outfits = mkOption {
      type = types.attrsOf toml.type;
      default = { };
      description = "Declared outfits rendered under figaro/outfits. Never put credentials here; the Nix store is public.";
    };

    roleApplications = mkOption {
      type = types.attrsOf applicationType;
      default = { };
      description = "Outfit applications to existing roles. Live forms remain authoritative; this is not a role registry.";
    };

    roleApplicationRuntimeDir = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Explicit local daemon runtime directory for declared role applications.";
    };

    applyRolesOnSwitch = lib.mkEnableOption "applying or checking declared role fields after outfit installation";

    restartOnSwitch = mkOption {
      type = types.bool;
      default = false;
      description = ''
        After a switch that changes the figaro build, restart the running
        angelus on the new build, keeping its pid bindings, and restart each
        figaro in a tmux pane that cannot reconnect by itself (builds before
        0.54.3): the pane's own process with respawn-pane, a shell's command
        typed again once that shell holds the terminal. A client that sent a
        prompt comes back as figaro listen on its aria, never sending the
        prompt again. Panes without figaro, piped or --stdin clients and
        anything outside tmux are named and left alone. When turns are still running
        after restartWait seconds nothing restarts; the switch never fails
        because of this step.
      '';
    };

    restartWait = mkOption {
      type = types.ints.unsigned;
      default = 30;
      description = "Seconds to wait for running turns before leaving the old angelus in place.";
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      {
        assertions = [
          {
            assertion = cfg.roleApplications == { } || cfg.roleApplicationRuntimeDir != null;
            message = "Figaro role applications require an explicit local runtime directory.";
          }
          {
            assertion = lib.all (app: app.outfits != [ ] && app.managed != [ ]) (
              lib.attrValues cfg.roleApplications
            );
            message = "Each Figaro role application needs outfit names and managed paths.";
          }
          {
            assertion = !cfg.applyRolesOnSwitch || cfg.roleApplications != { };
            message = "applyRolesOnSwitch requires at least one declared role application.";
          }
        ];
        home.packages = [ cfg.package ] ++ lib.optional (cfg.roleApplications != { }) roleApplications;
        xdg.configFile = lib.mapAttrs' (
          name: spec:
          lib.nameValuePair "figaro/outfits/${name}.toml" {
            source = toml.generate "figaro-outfit-${name}.toml" spec;
          }
        ) cfg.outfits;
      }
      (lib.mkIf cfg.applyRolesOnSwitch {
        home.activation.figaroApplyRoles = lib.hm.dag.entryAfter [ "linkGeneration" ] ''
          figaro_role_check_args=()
          if [ -n "''${DRY_RUN:-}" ]; then
            figaro_role_check_args=(--check)
          fi
          if ! ${roleApplications}/bin/figaro-apply-roles "''${figaro_role_check_args[@]}"; then
            echo "figaro: role application did not complete; run ${roleApplications}/bin/figaro-apply-roles --check before retrying" >&2
          fi
        '';
      })
      (lib.mkIf cfg.restartOnSwitch {
        home.activation.figaroRestartOnSwitch = lib.hm.dag.entryAfter [ "linkGeneration" ] ''
          if [ -n "''${DRY_RUN:-}" ]; then
            ${restart} -n || true
          else
            ${restart} || echo "figaro: the angelus stays on its old build; to move it: ${figaro} upgrade --to ${figaro}" >&2
          fi
        '';
      })
    ]
  );
}
