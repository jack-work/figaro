{ config, lib, pkgs, ... }:
let
  upstream = "gluck.tail34d120.ts.net:7482";
  relayService = "approvals-relay";
  tokenSecret = "approvals-edge-token";

  relayConfig = pkgs.writeText "approvals-relay.caddyfile" ''
    {
      admin off
      persist_config off
      auto_https off
      storage file_system /run/${relayService}
    }

    http:// {
      bind fd/3

      route {
        request_header -X-Figaro-Approvals-Edge
        @not_get_post not method GET POST
        respond @not_get_post 405
        reverse_proxy ${upstream} {
          header_up Host {upstream_hostport}
          header_up X-Figaro-Approvals-Edge "{env.APPROVALS_EDGE_TOKEN}"
          header_up -Authorization
          header_up -Cookie
        }
      }
    }
  '';
in
{
  sops.secrets.${tokenSecret} = { };
  sops.templates."approvals-relay.env".content = ''
    APPROVALS_EDGE_TOKEN=${config.sops.placeholder.${tokenSecret}}
  '';

  services.kelliher-web.sites.approvals = {
    subdomains = [ "approvals" ];
    requireAuth = true;
    trustsRemoteHeaders = true;
    proxySocket = "${relayService}.service";
  };

  systemd.services.${relayService} = {
    description = "approvals.kelliher.info: gluck's owner approvals page, vouched for by the edge secret";
    after = [ "network-online.target" "tailscaled.service" ];
    wants = [ "network-online.target" ];
    restartTriggers = [ config.sops.templates."approvals-relay.env".content ];
    serviceConfig = {
      ExecStart = "${pkgs.caddy}/bin/caddy run --adapter caddyfile --config ${relayConfig}";
      EnvironmentFile = config.sops.templates."approvals-relay.env".path;
      DynamicUser = true;
      RuntimeDirectory = relayService;
      Environment = [
        "XDG_DATA_HOME=/run/${relayService}"
        "XDG_CONFIG_HOME=/run/${relayService}"
      ];
      Restart = "on-failure";
      RestartSec = 5;
      NoNewPrivileges = true;
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      PrivateUsers = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectKernelLogs = true;
      ProtectControlGroups = true;
      RestrictNamespaces = true;
      RestrictRealtime = true;
      RestrictSUIDSGID = true;
      LockPersonality = true;
      MemoryDenyWriteExecute = true;
      CapabilityBoundingSet = "";
      SystemCallArchitectures = "native";
      SystemCallFilter = [ "@system-service" ];
      RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
      IPAddressDeny = "any";
      IPAddressAllow = [ "127.0.0.1" "::1" "100.64.0.0/10" "fd7a:115c:a1e0::/48" ];
      SocketBindDeny = "any";
    };
  };
}
