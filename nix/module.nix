{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.services.figaro;
  inherit (lib) mkOption types;
  toml = pkgs.formats.toml { };
  configDir = pkgs.linkFarm "figaro-config" (
    lib.optional (cfg.settings != { }) {
      name = "config.toml";
      path = toml.generate "config.toml" cfg.settings;
    }
    ++ lib.mapAttrsToList (name: spec: {
      name = "providers/${name}.toml";
      path = toml.generate "${name}.toml" spec;
    }) cfg.providers
  );
in
{
  options.services.figaro = {
    enable = lib.mkEnableOption "the figaro angelus as a system service";

    package = mkOption {
      type = types.package;
      description = "The figaro package.";
    };

    user = mkOption {
      type = types.str;
      default = "figaro";
    };

    uid = mkOption {
      type = types.int;
      default = 2001;
      description = "Pinned so the store's ownership survives a rebuild and a host can map it.";
    };

    gid = mkOption {
      type = types.int;
      default = 2001;
    };

    stateDir = mkOption {
      type = types.str;
      default = "/var/lib/figaro";
      description = "The store, and HOME for the service user.";
    };

    runtimeDir = mkOption {
      type = types.str;
      default = "/run/figaro";
      description = "Where the angelus binds its sockets.";
    };

    settings = mkOption {
      type = toml.type;
      default = { };
      description = "config.toml, rendered into a read-only config directory.";
    };

    providers = mkOption {
      type = types.attrsOf toml.type;
      default = { };
      description = "Declared providers, rendered as providers/<name>.toml. Never put a key here; use credentials.";
    };

    credentials = mkOption {
      type = types.attrsOf types.str;
      default = { };
      example = { anthropic = ""; };
      description = ''
        Credential id -> source for LoadCredential. An empty source imports
        a system credential of that id (what a container receives from
        systemd-nspawn --load-credential); otherwise it is a host path.
        figaro reads a provider's key from $CREDENTIALS_DIRECTORY/<provider>.
      '';
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
        bubblewrap
      ];
      description = "On PATH for the angelus and therefore for every tool call.";
    };

    memoryMax = mkOption {
      type = types.nullOr types.str;
      default = null;
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.${cfg.user} = {
      isSystemUser = true;
      uid = cfg.uid;
      group = cfg.user;
      home = cfg.stateDir;
    };
    users.groups.${cfg.user}.gid = cfg.gid;

    systemd.tmpfiles.rules = [ "d ${cfg.stateDir} 0700 ${cfg.user} ${cfg.user} -" ];

    systemd.services.figaro = {
      description = "figaro angelus";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      path = cfg.toolPackages;
      environment = {
        HOME = cfg.stateDir;
        FIGARO_STATE_DIR = cfg.stateDir;
        FIGARO_CACHE_DIR = "${cfg.stateDir}/cache";
        FIGARO_CONFIG_DIR = "${configDir}";
        FIGARO_RUNTIME_DIR = cfg.runtimeDir;
        XDG_RUNTIME_DIR = cfg.runtimeDir;
        FIGARO_NO_SELF_SPAWN = "1";
      };
      serviceConfig = {
        Type = "exec";
        User = cfg.user;
        Group = cfg.user;
        ExecStart = "${lib.getExe cfg.package} --angelus";
        Restart = "on-failure";
        RestartSec = 5;
        KillMode = "mixed";
        TimeoutStopSec = 30;
        RuntimeDirectory = lib.removePrefix "/run/" cfg.runtimeDir;
        RuntimeDirectoryMode = "0750";
        RuntimeDirectoryPreserve = true;
        ReadWritePaths = [ cfg.stateDir ];
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        UMask = "0077";
        LoadCredential = lib.mapAttrsToList (id: src: "${id}:${src}") (lib.filterAttrs (_: src: src != "") cfg.credentials);
        ImportCredential = lib.attrNames (lib.filterAttrs (_: src: src == "") cfg.credentials);
      } // lib.optionalAttrs (cfg.memoryMax != null) { MemoryMax = cfg.memoryMax; };
    };

    environment.systemPackages = [ cfg.package ];
    environment.variables = {
      FIGARO_RUNTIME_DIR = cfg.runtimeDir;
      FIGARO_NO_SELF_SPAWN = "1";
    };
  };
}
