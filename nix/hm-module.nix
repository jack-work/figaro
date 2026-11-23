{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.figaro;
  d = cfg.daemon;
  inherit (lib) mkOption types;
  systemdQuote = s: "\"${lib.escape [ "\\" "\"" ] s}\"";
  daemonPath = lib.concatStringsSep ":" (
    map (p: "${p}/bin") d.path ++ [ "%h/.nix-profile/bin" "%h/.local/bin" ] ++ d.pathExtra
  );
  daemonEnvironment = lib.mapAttrsToList (n: v: systemdQuote "${n}=${v}") (
    {
      FIGARO_NO_SELF_SPAWN = "1";
      FIGARO_RUNTIME_DIR = "%t/figaro";
      PATH = daemonPath;
    }
    // lib.optionalAttrs (d.sshAuthSock != null) { SSH_AUTH_SOCK = d.sshAuthSock; }
    // d.environment
  );
  toml = pkgs.formats.toml { };
  figaro = "${cfg.package}/bin/figaro";
  restart = "${figaro} upgrade --to ${figaro} --if-changed --wait ${toString cfg.restartWait}";
  applicationType = types.submodule {
    options = {
      roleId = mkOption {
        type = types.strMatching "@([0-9a-f]{8}|[0-9a-f]{32})";
        description = "Exact existing role ID (@ and 32 hex digits, or 8 for a legacy role), not a name or prefix.";
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
        prompt again. With daemon.enable the unit is what restarts, and this
        step runs after the switch has loaded the new unit file. Panes without figaro, piped or --stdin clients and
        anything outside tmux are named and left alone. When turns are still running
        after restartWait seconds nothing restarts; the switch never fails
        because of this step.
      '';
    };

    daemon = {
      enable = lib.mkEnableOption ''
        the angelus as the user unit figaro.service, restarted by systemd and
        started with the user manager, instead of a daemon the first CLI
        spawns with its own environment. CLIs under FIGARO_NO_SELF_SPAWN
        (set for every session) connect to it and never start another
      '';

      path = mkOption {
        type = types.listOf types.package;
        default = [ ];
        description = "Packages whose bin directories lead the daemon's PATH, and so every tool call's.";
      };

      pathExtra = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [
          "/usr/local/bin"
          "/usr/bin"
        ];
        description = "Directories after ~/.nix-profile/bin and ~/.local/bin on the daemon's PATH, for tools a distribution owns. Systemd specifiers expand.";
      };

      environment = mkOption {
        type = types.attrsOf types.str;
        default = { };
        description = "Extra non-secret variables. Values are quoted for systemd but specifiers (%t, %h) still expand. Overrides the generated ones, PATH included.";
      };

      environmentFile = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "%h/.config/figaro/daemon.env";
        description = "A file of KEY=value secrets read at start, kept out of the store. A missing file is ignored, so a provider that wants a key refuses requests until it exists; the daemon still starts.";
      };

      sshAuthSock = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "%t/fleet-ssh-agent.sock";
        description = "SSH_AUTH_SOCK for the daemon and everything it runs. %t is the user runtime directory, expanded by systemd.";
      };

      socketActivation = mkOption {
        type = types.bool;
        default = true;
        description = ''
          Let figaro.socket hold the CLI door, %t/figaro/angelus.sock. It
          survives a restart, so a client dialling during one waits and is
          answered, and a call made while the daemon is down starts it.
        '';
      };

      memoryMax = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "6G";
      };

      wants = mkOption {
        type = types.listOf types.str;
        default = [ ];
        example = [ "fleet-ssh-agent.service" ];
        description = "Units figaro.service pulls in (Wants=) and starts after (After=).";
      };
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
        home.activation.figaroApplyRoles = lib.hm.dag.entryAfter [ "linkGeneration" "figaroRestartOnSwitch" ] ''
          figaro_role_check_args=()
          if [ -n "''${DRY_RUN:-}" ]; then
            figaro_role_check_args=(--check)
          fi
          if ! ${roleApplications}/bin/figaro-apply-roles "''${figaro_role_check_args[@]}"; then
            echo "figaro: role application did not complete; run ${roleApplications}/bin/figaro-apply-roles --check before retrying" >&2
          fi
        '';
      })
      (lib.mkIf d.enable {
        home.sessionVariables.FIGARO_NO_SELF_SPAWN = "1";

        systemd.user.sockets.figaro = lib.mkIf d.socketActivation {
          Unit = {
            Description = "figaro angelus CLI door";
            X-SwitchMethod = "keep-old";
          };
          Socket = {
            ListenStream = "%t/figaro/angelus.sock";
            FileDescriptorName = "angelus";
            SocketMode = "0600";
            DirectoryMode = "0700";
            RemoveOnStop = true;
            TriggerLimitIntervalSec = 0;
            PollLimitIntervalSec = "10s";
            PollLimitBurst = 3;
          };
          Install.WantedBy = [ "sockets.target" ];
        };

        systemd.user.services.figaro = {
          Unit = {
            Description = "figaro angelus";
            After = lib.optional d.socketActivation "figaro.socket" ++ d.wants;
            Wants = d.wants;
            Requires = lib.optional d.socketActivation "figaro.socket";
            X-SwitchMethod = "keep-old";
          }
          // lib.optionalAttrs d.socketActivation { StartLimitIntervalSec = 0; };
          Service = {
            Type = "exec";
            ExecStart = "${figaro} --angelus";
            Restart = "on-failure";
            RestartSec = 5;
            RestartSteps = 6;
            RestartMaxDelaySec = "5min";
            KillMode = "mixed";
            TimeoutStopSec = 30;
            OOMPolicy = "continue";
            RuntimeDirectory = "figaro";
            RuntimeDirectoryMode = "0700";
            RuntimeDirectoryPreserve = true;
            Environment = daemonEnvironment;
          }
          // lib.optionalAttrs d.socketActivation { Sockets = "figaro.socket"; }
          // lib.optionalAttrs (d.environmentFile != null) { EnvironmentFile = "-${d.environmentFile}"; }
          // lib.optionalAttrs (d.memoryMax != null) { MemoryMax = d.memoryMax; };
          Install.WantedBy = [ "default.target" ];
        };
      })
      (lib.mkIf cfg.restartOnSwitch {
        home.activation.figaroRestartOnSwitch = lib.hm.dag.entryAfter (
          [ "linkGeneration" ] ++ lib.optional d.enable "reloadSystemd"
        ) ''
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
