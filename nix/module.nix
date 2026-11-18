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
  doorUsers = lib.unique (lib.optional (cfg.owner != null) cfg.owner ++ cfg.users);
  uidOf = u: config.users.users.${u}.uid;
  principalOf = u: "user:${toString (uidOf u)}";
  doorGroup = if doorUsers != [ ] then "figaro-door" else cfg.user;
  runtimeMode = if doorUsers != [ ] then "0751" else "0750";
  configDir = pkgs.linkFarm "figaro-config" (
    lib.optional (cfg.settings != { }) {
      name = "config.toml";
      path = toml.generate "config.toml" cfg.settings;
    }
    ++ lib.mapAttrsToList (name: spec: {
      name = "providers/${name}.toml";
      path = toml.generate "${name}.toml" spec;
    }) cfg.providers
    ++ lib.mapAttrsToList (name: spec: {
      name = "outfits/${name}.toml";
      path = toml.generate "${name}.toml" spec;
    }) cfg.outfits
  );
in
{
  options.services.figaro = {
    enable = lib.mkEnableOption ''
      the figaro angelus as a system service. A switch that changes the
      package restarts the unit through systemd, and no terminal clients are
      attached to it, so this module needs no restart step of its own
      (programs.figaro.restartOnSwitch in the home-manager module is for a
      user's own angelus and its tmux panes)'';

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

    mode = mkOption {
      type = types.enum [
        "house"
        "estate"
      ];
      default = if doorUsers != [ ] then "house" else "estate";
      defaultText = lib.literalExpression ''if owner or users name a person then "house" else "estate"'';
      description = ''
        house: a person's machine. The angelus runs as `user`, the person
        named by `owner` owns it, the people in `users` reach its CLI door,
        and the CLI never starts a daemon of its own. estate: a server with
        no person's files in play. In both, tools run only in boxes unless
        `localTools` is set, since a tool run as the daemon's uid can rewrite
        every figaro's store.

        Moving a host from home mode to house mode breaks: the store moves
        (see `importFrom`), ~/.config/figaro is no longer read (config is
        rendered here), a figaro on `local` or naming no sandbox is refused
        when it opens until `system.sandbox` names a box, and the daemon's
        own uid holds nothing at its doors.
      '';
    };

    owner = mkOption {
      type = types.nullOr types.str;
      default = null;
      example = "gluck";
      description = ''
        The user who owns this daemon, rendered as `[authz] owner =
        "user:<uid>"`: the default policy gives him every verb, and the
        operator paths (managed fan-out, studying a figaro) are his. He
        reaches the CLI door like every user in `users`. Required in house
        mode; null in estate mode keeps the daemon's own uid as the owner.
      '';
    };

    users = mkOption {
      type = types.listOf types.str;
      default = [ ];
      example = [ "alice" ];
      description = ''
        Users, besides the owner, who may reach the CLI door: put in group
        figaro-door and admitted by peer credentials as `user:<uid>`, holding
        what the policy gives them (the default policy gives them nothing).
        Each needs a pinned `users.users.<name>.uid`.
      '';
    };

    localTools = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Run the tools of a figaro whose system.sandbox is local, or unset, as
        the daemon's own uid. False renders `[sandbox] local_tools = false`
        and such a figaro is refused when it opens, with a message naming
        the way out.
      '';
    };

    socketActivation = mkOption {
      type = types.bool;
      default = cfg.mode == "house";
      defaultText = lib.literalExpression ''config.services.figaro.mode == "house"'';
      description = ''
        Let systemd hold the CLI door, `''${runtimeDir}/angelus.sock`, in
        figaro.socket and start the angelus on the first connection instead
        of at boot. A call made while the service is stopped waits in the
        socket's queue and is answered by the start it triggers; stopping
        the service leaves the socket in place. Admission stays the
        daemon's: every connection is still judged by peer credentials and
        policy. False binds the socket in the daemon, as before.

        A daemon that cannot start never fails the socket: calls waiting on
        it trigger at most three starts in ten seconds, systemd retries in
        the background with a backoff growing to five minutes, and fixing
        the cause is enough to bring the door back.
      '';
    };

    socketMode = mkOption {
      type = types.str;
      default = if doorUsers != [ ] then "0660" else "0600";
      defaultText = lib.literalExpression ''if owner or users name a person then "0660" else "0600"'';
      example = "0660";
      description = "Mode of the CLI door when figaro.socket holds it; its group is figaro-door when owner or users name a person, else the service user's group.";
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

    outfits = mkOption {
      type = types.attrsOf toml.type;
      default = { };
      example = { guest = { system = { ttl = "24h"; keepalive = "24h"; }; }; };
      description = "Declared outfits, rendered as outfits/<name>.toml; `figaro new -O <name>` dresses with one.";
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
        A host path that is missing or unreadable at start leaves the
        credential empty instead of failing the unit: the provider naming it
        refuses each request as not provisioned, and every other provider
        keeps working. Restart the unit once the file exists.
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
      ];
      description = "On PATH for the angelus and therefore for every tool call.";
    };

    memoryMax = mkOption {
      type = types.nullOr types.str;
      default = null;
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.mode != "house" || cfg.owner != null;
        message = "services.figaro.mode = \"house\" needs services.figaro.owner: the daemon runs as ${cfg.user}, which is not a person.";
      }
      {
        assertion = doorUsers == [ ] || cfg.socketActivation;
        message = "services.figaro.owner and users reach the CLI door through figaro.socket, which sets its group and mode: set services.figaro.socketActivation = true.";
      }
    ]
    ++ map (u: {
      assertion = uidOf u != null;
      message = "services.figaro: ${u} reaches the CLI door as user:<uid>, so users.users.${u}.uid must be pinned.";
    }) doorUsers;

    users.users = lib.mkMerge [
      {
        ${cfg.user} = {
          isSystemUser = true;
          uid = cfg.uid;
          group = cfg.user;
          home = cfg.stateDir;
        };
      }
      (lib.genAttrs doorUsers (_: {
        extraGroups = [ "figaro-door" ];
      }))
    ];
    users.groups = {
      ${cfg.user}.gid = cfg.gid;
    }
    // lib.optionalAttrs (doorUsers != [ ]) { figaro-door = { }; };

    services.figaro.settings = lib.mkMerge [
      { sandbox.local_tools = lib.mkDefault cfg.localTools; }
      (lib.mkIf (cfg.owner != null) { authz.owner = lib.mkDefault (principalOf cfg.owner); })
      (lib.mkIf (doorUsers != [ ]) {
        authz.peercred.uids = lib.listToAttrs (map (u: lib.nameValuePair (toString (uidOf u)) (principalOf u)) doorUsers);
      })
    ];

    systemd.tmpfiles.rules = [ "d ${cfg.stateDir} 0700 ${cfg.user} ${cfg.user} -" ]
      ++ lib.optionals cfg.socketActivation [
        "d ${cfg.runtimeDir} ${runtimeMode} ${cfg.user} ${cfg.user} -"
        "z ${cfg.runtimeDir}/angelus.sock ${cfg.socketMode} ${cfg.user} ${doorGroup} -"
      ];

    systemd.sockets.figaro = lib.mkIf cfg.socketActivation {
      description = "figaro angelus CLI door";
      wantedBy = [ "sockets.target" ];
      listenStreams = [ "${cfg.runtimeDir}/angelus.sock" ];
      socketConfig = {
        FileDescriptorName = "angelus";
        SocketUser = cfg.user;
        SocketGroup = doorGroup;
        SocketMode = cfg.socketMode;
        DirectoryMode = runtimeMode;
        RemoveOnStop = true;
        TriggerLimitIntervalSec = 0;
        PollLimitIntervalSec = "10s";
        PollLimitBurst = 3;
      };
    };

    systemd.services.figaro = {
      description = "figaro angelus";
      wantedBy = lib.optional (!cfg.socketActivation) "multi-user.target";
      after = [ "network-online.target" ] ++ lib.optional cfg.socketActivation "figaro.socket";
      wants = [ "network-online.target" ];
      requires = lib.optional cfg.socketActivation "figaro.socket";
      startLimitIntervalSec = lib.mkIf cfg.socketActivation 0;
      restartTriggers = lib.optional cfg.socketActivation config.systemd.units."figaro.socket".unit;
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
        RuntimeDirectoryMode = runtimeMode;
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
        SetCredential = lib.mapAttrsToList (id: _: "${id}:") (lib.filterAttrs (_: src: src != "") cfg.credentials);
        ImportCredential = lib.attrNames (lib.filterAttrs (_: src: src == "") cfg.credentials);
      } // lib.optionalAttrs cfg.socketActivation {
        Sockets = "figaro.socket";
        RestartSteps = 6;
        RestartMaxDelaySec = "5min";
      }
        // lib.optionalAttrs (cfg.memoryMax != null) { MemoryMax = cfg.memoryMax; };
    };

    environment.systemPackages = [ cfg.package ];
    environment.variables = {
      FIGARO_RUNTIME_DIR = cfg.runtimeDir;
      FIGARO_NO_SELF_SPAWN = "1";
    };
  };
}
