{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.figaro-valet;
  figaro = config.services.figaro;
  inherit (lib) mkOption types;
  peerGroup = if figaro.enable then figaro.user else null;
  args =
    [ "valet" "serve" "--max-grant" cfg.maxGrant ]
    ++ lib.concatMap (p: [ "--peer" p ]) cfg.peers
    ++ lib.concatMap (p: [ "--hide" p ]) cfg.hide
    ++ lib.optionals (cfg.door != null) [ "--door" cfg.door ];
in
{
  options.services.figaro-valet = {
    users = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "alice" ];
      description = ''
        Users who get a valet: figaro-valet.socket in their own systemd
        manager, at /run/figaro-valet/<user>/exec.sock, starting one
        figaro-valet@ instance, in its own cgroup, per connection. The daemon
        reaches it as a box: `[sandbox.boxes.<name>] exec` names the socket and
        an outfit picks it with system.sandbox. Give each user linger if the
        valet must answer while they are logged out. The person stops it with
        `systemctl --user stop figaro-valet.socket`.
      '';
    };

    package = mkOption {
      type = types.package;
      description = "The figaro package; the valet is its `valet` subcommand.";
    };

    peers = mkOption {
      type = types.listOf types.str;
      default = lib.optional figaro.enable figaro.user;
      defaultText = lib.literalExpression ''lib.optional config.services.figaro.enable config.services.figaro.user'';
      description = ''
        Users or uids whose connections the valet answers, checked by peer
        credentials. Empty admits only the person, which is home mode: the
        daemon runs as them.
      '';
    };

    maxGrant = mkOption {
      type = types.str;
      default = "7d";
      description = "The longest grant the valet honours, counted from when it was made.";
    };

    hide = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "%h/dev/cheroot-config" "%h/.config/home-manager" ];
      description = ''
        Paths no tool sees even when a grant holds them, beside the built-in
        ~/.ssh, ~/.gnupg, figaro's own state, cache and config, and the
        valet's grant table. systemd specifiers (%h, %u) expand.
      '';
    };

    door = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "/run/figaro/boxes/valet-%u/door.sock";
      description = "The daemon's door for this valet's box, bound into the fence so figaro inside a tool calls back through it.";
    };

    toolPackages = mkOption {
      type = types.listOf types.package;
      default = with pkgs; [
        bash
        coreutils
        findutils
        gnugrep
        gnused
        git
        cfg.package
      ];
      defaultText = lib.literalExpression "with pkgs; [ bash coreutils findutils gnugrep gnused git cfg.package ]";
      description = "On PATH inside every tool call the valet runs.";
    };
  };

  config = lib.mkIf (cfg.users != [ ]) {
    systemd.tmpfiles.rules = [ "d /run/figaro-valet 0755 root root -" ]
      ++ map (u:
        if peerGroup != null then "d /run/figaro-valet/${u} 0750 ${u} ${peerGroup} -"
        else "d /run/figaro-valet/${u} 0700 ${u} - -") cfg.users;

    systemd.user.sockets.figaro-valet = {
      description = "figaro valet for %u";
      wantedBy = [ "sockets.target" ];
      unitConfig.ConditionUser = map (u: "|${u}") cfg.users;
      socketConfig = {
        ListenStream = "/run/figaro-valet/%u/exec.sock";
        Accept = true;
        FileDescriptorName = "connection";
        SocketMode = "0666";
        RemoveOnStop = true;
      };
    };

    systemd.user.services."figaro-valet@" = {
      description = "figaro valet call for %u";
      path = cfg.toolPackages;
      serviceConfig = {
        Type = "notify";
        NotifyAccess = "all";
        MemoryAccounting = true;
        ExecStart = "${lib.getExe cfg.package} ${lib.escapeShellArgs args}";
        KillMode = "control-group";
        CollectMode = "inactive-or-failed";
      };
    };
  };
}
