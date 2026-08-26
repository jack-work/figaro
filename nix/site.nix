# Platform registration: figaro as a kelliher-web site.
#
# SEPARATE FROM module.nix ON PURPOSE. The daemon module describes how to run
# figaro and works on any NixOS host. This one couples to a specific
# platform's option surface, and a host that does not run kelliher-web must
# not have to evaluate it -- a `mkIf` guard cannot express that, because the
# module system asserts an option path exists before it consults the
# condition.
#
# spain imports both. Anything else imports only the first.
{ config, lib, ... }:
let cfg = config.services.figaro;
in {
  config = lib.mkIf cfg.enable {
    services.kelliher-web.sites.figaro = {
      subdomains = [ cfg.subdomain ];
      requireAuth = true;
      inherit (cfg) requiredGroups;
      # Loopback: Caddy is the only thing that can reach the gateway, which
      # is what makes `--authn upstream` believable. The gateway itself
      # refuses that authenticator on any address that is not loopback or a
      # unix socket, so this pairing is enforced on both sides.
      proxyTo = cfg.port;
    };
  };
}
