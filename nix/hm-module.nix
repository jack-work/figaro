{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.figaro;
  inherit (lib) mkOption types;
  figaro = "${cfg.package}/bin/figaro";
  restart = "${figaro} upgrade --to ${figaro} --if-changed --wait ${toString cfg.restartWait}";
in
{
  options.programs.figaro = {
    enable = lib.mkEnableOption "figaro";

    package = mkOption {
      type = types.package;
      description = "The figaro package.";
    };

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
      { home.packages = [ cfg.package ]; }
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
