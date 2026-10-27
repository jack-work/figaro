{
  config,
  lib,
  ...
}:
let
  cfg = config.services.figaro-container;
  inherit (lib) mkOption types;
  hostUid = cfg.uidBase + cfg.uid;
  hostGid = cfg.uidBase + cfg.gid;
  fw = config.networking.firewall;
  iface = "ve-figaro";
  open =
    proto:
    let
      own = fw.interfaces.${iface} or { };
      ports = fw."allowed${proto}Ports" ++ own."allowed${proto}Ports" or [ ];
      ranges = fw."allowed${proto}PortRanges" ++ own."allowed${proto}PortRanges" or [ ];
    in
    map toString ports ++ map (r: "${toString r.from}:${toString r.to}") ranges;
  families = ''
    v4="iptables -w"
    v6="ip6tables -w"
  '';
  returns =
    proto:
    lib.concatMapStrings (p: ''
      $t -A figaro-guard-in -p ${lib.toLower proto} --dport ${p} -j RETURN
    '') (open proto);
in
{
  options.services.figaro-container = {
    enable = lib.mkEnableOption "the figaro angelus in its own nspawn container";

    package = mkOption {
      type = types.package;
      description = "The figaro package run inside the container.";
    };

    uidBase = mkOption {
      type = types.ints.unsigned;
      default = 524288;
      description = ''
        The container's private user range starts here (a multiple of 65536,
        above every host uid). figaro's pinned uid inside maps to
        uidBase + uid on the host, which owns the state and door directories.
      '';
    };

    uid = mkOption {
      type = types.int;
      default = 2001;
      description = "figaro's uid inside the container, pinned.";
    };

    gid = mkOption {
      type = types.int;
      default = 2001;
    };

    stateDir = mkOption {
      type = types.str;
      default = "/var/lib/figaro";
      description = "Host directory bind-mounted as the container's store.";
    };

    doorDir = mkOption {
      type = types.str;
      default = "/run/figaro-door";
      description = "Host directory bind-mounted as the angelus' runtime directory, where its sockets appear.";
    };

    hostAddress = mkOption {
      type = types.str;
      default = "10.233.1.1";
    };

    localAddress = mkOption {
      type = types.str;
      default = "10.233.1.2";
    };

    externalInterface = mkOption {
      type = types.str;
      description = "The host interface the container's egress is NATed out of.";
    };

    nameservers = mkOption {
      type = types.listOf types.str;
      default = [ "1.1.1.1" "9.9.9.9" ];
    };

    blockedDestinations = mkOption {
      type = types.listOf types.str;
      default = [ "10.0.0.0/8" "172.16.0.0/12" "192.168.0.0/16" "100.64.0.0/10" "169.254.0.0/16" ];
      description = "Forwarded traffic from the container to these is dropped: the LAN, the tailnet, link-local.";
    };

    memoryMax = mkOption {
      type = types.str;
      default = "4G";
    };

    cpuWeight = mkOption {
      type = types.ints.between 1 10000;
      default = 20;
    };

    ioWeight = mkOption {
      type = types.ints.between 1 10000;
      default = 20;
    };

    credentials = mkOption {
      type = types.attrsOf types.str;
      default = { };
      example = { anthropic = "/run/secrets/figaro-anthropic-key"; };
      description = ''
        Credential id -> host path. Loaded into container@figaro by
        LoadCredential, passed into the container by --load-credential, and
        imported by the angelus unit there. A path missing at start passes an
        empty credential instead of failing the container; the provider naming
        it refuses requests as not provisioned.
      '';
    };

    figaro = mkOption {
      type = types.deferredModule;
      default = { };
      description = "Extra configuration for services.figaro inside the container (settings, providers, toolPackages).";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = lib.mod cfg.uidBase 65536 == 0 && cfg.uidBase >= 65536;
        message = "services.figaro-container.uidBase must be a multiple of 65536 above the host's uid range.";
      }
      {
        assertion = lib.all (p: lib.hasPrefix "/" p) (lib.attrValues cfg.credentials);
        message = "services.figaro-container.credentials take host PATHS, never values.";
      }
    ];

    systemd.tmpfiles.rules = [
      "d ${cfg.stateDir} 0700 ${toString hostUid} ${toString hostGid} -"
      "d ${cfg.doorDir} 0750 ${toString hostUid} ${toString hostGid} -"
    ];

    systemd.slices.figaro = {
      description = "figaro and everything it runs";
      sliceConfig = {
        MemoryMax = cfg.memoryMax;
        CPUWeight = cfg.cpuWeight;
        IOWeight = cfg.ioWeight;
      };
    };

    systemd.services.figaro-guard-net = {
      description = "The figaro container's forward and input filter";
      path = [ fw.package ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      script = ''
        set -euo pipefail
        ${families}
        for t in "$v4" "$v6"; do
          $t -N figaro-guard 2>/dev/null || $t -F figaro-guard
          $t -N figaro-guard-in 2>/dev/null || $t -F figaro-guard-in
        done
        ${lib.concatMapStrings (d: ''
          $v4 -A figaro-guard -d ${d} -j DROP
        '') cfg.blockedDestinations}
        $v6 -A figaro-guard -j DROP
        for t in "$v4" "$v6"; do
          $t -A figaro-guard-in -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
          ${returns "TCP"}
          ${returns "UDP"}
        done
        ${lib.optionalString fw.allowPing ''
          $v4 -A figaro-guard-in -p icmp --icmp-type echo-request -j RETURN
        ''}
        $v6 -A figaro-guard-in -p ipv6-icmp -j RETURN
        for t in "$v4" "$v6"; do
          $t -A figaro-guard-in -j DROP
          $t -C FORWARD -i ${iface} -j figaro-guard 2>/dev/null || $t -I FORWARD 1 -i ${iface} -j figaro-guard
          $t -C INPUT -i ${iface} -j figaro-guard-in 2>/dev/null || $t -I INPUT 1 -i ${iface} -j figaro-guard-in
        done
      '';
      preStop = ''
        ${families}
        for t in "$v4" "$v6"; do
          $t -D FORWARD -i ${iface} -j figaro-guard 2>/dev/null || true
          $t -D INPUT -i ${iface} -j figaro-guard-in 2>/dev/null || true
        done
      '';
    };

    systemd.services."container@figaro" = {
      bindsTo = [ "figaro-guard-net.service" ];
      after = [ "figaro-guard-net.service" ];
      serviceConfig = {
        Slice = lib.mkForce "figaro.slice";
        LoadCredential = lib.mapAttrsToList (id: path: "${id}:${path}") cfg.credentials;
        SetCredential = lib.mapAttrsToList (id: _: "${id}:") cfg.credentials;
      };
    };

    networking.nat = {
      enable = true;
      internalInterfaces = [ iface ];
      externalInterface = cfg.externalInterface;
    };

    containers.figaro = {
      autoStart = true;
      privateNetwork = true;
      inherit (cfg) hostAddress localAddress;
      privateUsers = cfg.uidBase;
      bindMounts = {
        "/var/lib/figaro" = {
          hostPath = cfg.stateDir;
          isReadOnly = false;
        };
        "/run/figaro" = {
          hostPath = cfg.doorDir;
          isReadOnly = false;
        };
      };
      extraFlags = [ "--private-users-ownership=chown" ]
        ++ map (id: "--load-credential=${id}:${id}") (lib.attrNames cfg.credentials);
      config = {
        imports = [ ./module.nix cfg.figaro ];
        services.figaro = {
          enable = true;
          package = cfg.package;
          inherit (cfg) uid gid;
          stateDir = "/var/lib/figaro";
          runtimeDir = "/run/figaro";
          credentials = lib.mapAttrs (_: _: "") cfg.credentials;
        };
        networking.useHostResolvConf = false;
        systemd.services.resolvconf.preStart = "rm -f /etc/resolv.conf";
        networking.nameservers = cfg.nameservers;
        networking.firewall.enable = true;
        system.stateVersion = "26.05";
      };
    };
  };
}
