# NixOS module: figaro as a hosted daemon.
#
# figaro is not a web app. It is a supervisor that owns a write-ahead log on
# disk and spawns in-process agents which RUN ARBITRARY SHELL COMMANDS and
# call LLM APIs with secret keys. It is closer to a CI runner than to a
# request handler, and every choice below follows from that.
#
# Two units, deliberately:
#
#   figaro-angelus.service   the daemon. Owns the store and both sockets.
#   figaro-gateway.service   the HTTP door. BindsTo the angelus, so a dead
#                            daemon takes the door down instead of leaving
#                            it 502ing into the void.
#
# On sandboxing, honestly: ProtectSystem and PrivateTmp are worth having.
# NoNewPrivileges and a syscall filter are NOT set, and their absence is a
# decision rather than an oversight -- this workload's PURPOSE is running
# arbitrary commands, so a sandbox tight enough to be meaningful breaks it
# and one loose enough to work is documentation rather than protection.
# What actually bounds the blast radius here is the dedicated user, the
# resource caps, and the fact that the door requires an authenticated admin.

{ config, lib, pkgs, ... }:

let
  cfg = config.services.figaro;
  inherit (lib) mkIf mkOption mkEnableOption types;

  # The angelus must NOT self-spawn under systemd. The CLI normally forks a
  # detached grandchild so a daemon outlives the shell that needed it; under
  # Type=exec that child escapes the unit's lifecycle and survives into the
  # next activation as an orphan holding the store lock.
  daemonEnv = {
    HOME = cfg.stateDir;
    XDG_RUNTIME_DIR = "/run/figaro";
    FIGARO_RUNTIME_DIR = "/run/figaro";
    FIGARO_STATE_DIR = cfg.stateDir;
    FIGARO_NO_SELF_SPAWN = "1";
  } // cfg.extraEnvironment;

  hardening = {
    # Worth having.
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    ProtectKernelTunables = true;
    ProtectControlGroups = true;
    RestrictSUIDSGID = true;
    # NOT set, on purpose -- see the header. Listing them here as comments
    # so nobody "fixes" the omission without reading why:
    #   NoNewPrivileges       breaks tools that legitimately escalate
    #   SystemCallFilter      breaks arbitrary command execution
    #   PrivateDevices        breaks anything wanting a tty or /dev/fd
  };
in
{
  options.services.figaro = {
    enable = mkEnableOption "figaro — a hosted agent daemon";

    package = mkOption {
      type = types.package;
      description = "The figaro package to run.";
    };

    user = mkOption {
      type = types.str;
      default = "figaro";
      description = ''
        Dedicated service user. NOT DynamicUser: that allocates a new uid per
        activation, and the figwal store is long-lived on-disk state whose
        ownership has to survive a redeploy.
      '';
    };

    stateDir = mkOption {
      type = types.path;
      default = "/var/lib/figaro";
      description = "Where the store lives. Also HOME for the service user.";
    };

    subdomain = mkOption {
      type = types.str;
      default = "fig";
      description = "Subdomain under the platform base domain.";
    };

    port = mkOption {
      type = types.port;
      default = 9098;
      description = ''
        LOOPBACK port for the gateway. It is loopback because the platform's
        Caddy proxies to host:port and has no unix-socket form; the gateway
        refuses `authn = upstream` on any address that is not loopback or a
        unix socket, so this is the one shape that is both reachable by
        Caddy and admitted by figaro.
      '';
    };

    hostname = mkOption {
      type = types.str;
      default = "fig.kelliher.info";
      description = ''
        The Host: header the gateway will accept. Binding loopback is NO
        defence against DNS rebinding -- a page in a browser on this machine
        can point a name it controls at 127.0.0.1 -- so the allowlist is what
        actually closes that door.
      '';
    };

    requiredGroups = mkOption {
      type = types.listOf types.str;
      default = [ "figaro-admin" ];
      description = ''
        lldap groups admitted. ONE group, deliberately: every figaro
        capability reduces to "runs arbitrary shell as the service user", so
        a read-only figaro role would be a lie. The platform creates the
        group from allRequiredGroups and gates 2FA from
        allAuthenticatedHostnames; neither needs a hand edit.
      '';
    };

    maxConnAge = mkOption {
      type = types.str;
      default = "8h";
      description = ''
        Cap on one tunnel's lifetime.

        THIS IS AN AUTHORIZATION CONTROL. Caddy's forward_auth runs on the
        UPGRADE REQUEST ONLY; once a WebSocket is established no frame is
        ever re-authorized, so a revoked operator keeps a live shell until
        the socket drops. Capping forces a re-upgrade, and a re-upgrade is
        re-authorized.
      '';
    };

    memoryMax = mkOption {
      type = types.str;
      default = "4G";
      description = ''
        Hard memory cap. Spain is the house router: a runaway agent must not
        be able to starve dnsmasq.
      '';
    };

    cpuQuota = mkOption {
      type = types.str;
      default = "200%";
      description = "CPU cap, for the same reason as memoryMax.";
    };

    providerKeyFiles = mkOption {
      type = types.attrsOf types.path;
      default = { };
      example = { anthropic = "/run/secrets/figaro-anthropic-key"; };
      description = ''
        Provider name -> path of a file containing that provider's API key.

        PATHS ONLY, NEVER VALUES. A value here would be rendered into the
        nix store, which is world-readable; the path is public and the file
        behind it is 0400 and owned by the service user. Point these at
        sops-nix outputs under /run/secrets.
      '';
    };

    extraEnvironment = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = "Extra environment for the daemon.";
    };
  };

  config = mkIf cfg.enable {
      assertions = [
        {
          assertion = !(lib.any (v: !(lib.hasPrefix "/" v)) (lib.attrValues cfg.providerKeyFiles));
          message = ''
            services.figaro.providerKeyFiles takes PATHS, not key material.
            A literal here would be copied into the world-readable nix store.
          '';
        }
        {
          assertion = cfg.requiredGroups != [ ];
          message = ''
            services.figaro.requiredGroups is empty, which would publish an
            unauthenticated door onto a daemon that runs shell commands.
          '';
        }
      ];

      users.users.${cfg.user} = {
        isSystemUser = true;
        group = cfg.user;
        home = cfg.stateDir;
        createHome = true;
        description = "figaro agent daemon";
      };
      users.groups.${cfg.user} = { };

      systemd.services.figaro-angelus = {
        description = "figaro — the angelus daemon";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];
        environment = daemonEnv;
        serviceConfig = hardening // {
          Type = "exec";
          User = cfg.user;
          Group = cfg.user;
          ExecStart = "${cfg.package}/bin/figaro --angelus";
          Restart = "on-failure";
          RestartSec = 5;
          StateDirectory = "figaro";
          RuntimeDirectory = "figaro";
          RuntimeDirectoryMode = "0700";
          ReadWritePaths = [ cfg.stateDir ];
          # Agents are grandchildren of this unit and they run shell. Without
          # `mixed` they are reparented to pid 1 on stop and survive into the
          # next activation, still holding file descriptors on the store.
          KillMode = "mixed";
          TimeoutStopSec = 30;
          MemoryMax = cfg.memoryMax;
          CPUQuota = cfg.cpuQuota;
          LoadCredential = lib.mapAttrsToList (n: p: "${n}:${p}") cfg.providerKeyFiles;
        };
      };

      systemd.services.figaro-gateway = {
        description = "figaro — the HTTP gateway";
        wantedBy = [ "multi-user.target" ];
        # BindsTo, not Requires: a dead angelus should take the door with it
        # rather than leave a listener returning 502 to an authenticated
        # admin who then has to guess why.
        bindsTo = [ "figaro-angelus.service" ];
        after = [ "figaro-angelus.service" ];
        environment = daemonEnv;
        serviceConfig = hardening // {
          Type = "exec";
          User = cfg.user;
          Group = cfg.user;
          ExecStart = lib.concatStringsSep " " [
            "${cfg.package}/bin/figaro serve"
            "--listen tcp://127.0.0.1:${toString cfg.port}"
            "--authn upstream"
            "--host ${cfg.hostname}"
            # THE GROUP GATE, and it is not redundant with the platform.
            # kelliher-web creates the lldap group from requiredGroups and
            # writes an Authelia rule from the hostname -- but that rule is
            # `policy: two_factor` with no subject restriction, so every
            # directory user who passes 2FA reaches this port. Authelia
            # authenticates; the app authorizes. This is the app.
            "--require-group ${lib.concatStringsSep "," cfg.requiredGroups}"
            "--max-conn-age ${cfg.maxConnAge}"
          ];
          Restart = "on-failure";
          RestartSec = 5;
          # It consumes the angelus's runtime dir; it must not create one.
          MemoryMax = "512M";
        };
      };
  };
}
