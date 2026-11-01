{ pkgs, self }:
pkgs.testers.runNixOSTest {
  name = "figaro-module";
  nodes.machine = {
    imports = [ self.nixosModules.default ];
    services.figaro = {
      enable = true;
      outfits.guest = {
        system = {
          ttl = "24h";
          keepalive = "24h";
        };
        greeting = "multi\nline \"quoted\" value";
      };
    };
  };

  testScript = ''
    import json

    machine.wait_for_unit("figaro.service")
    machine.wait_until_succeeds("test -S /run/figaro/angelus.sock || ls /run/figaro | grep -q sock")

    # The declared outfit lands where the daemon looks for outfits.
    env = machine.succeed("systemctl show figaro.service -p Environment --value")
    config_dir = [kv.split("=", 1)[1] for kv in env.split() if kv.startswith("FIGARO_CONFIG_DIR=")][0]
    machine.succeed(f"test -f {config_dir}/outfits/guest.toml")
    print(machine.succeed(f"cat {config_dir}/outfits/guest.toml"))

    # The daemon resolves the outfit by name and the keys land on the form.
    def fig(cmd):
        return machine.succeed(f"runuser -u figaro -- env HOME=/var/lib/figaro figaro {cmd}")

    form_id = json.loads(fig("form new -O guest -S probe=1 -j"))["form_id"]
    state = json.loads(fig(f"state show {form_id} -j"))
    print(state)

    def key(path):
        node = state
        for part in path.split("."):
            node = node[part]
        return node

    assert key("system.ttl") == "24h", state
    assert key("system.keepalive") == "24h", state
    assert key("greeting") == "multi\nline \"quoted\" value", state

    # An outfit the module did not declare is refused, not silently empty.
    machine.fail("runuser -u figaro -- env HOME=/var/lib/figaro figaro form new -O undeclared -S probe=1 -j")
  '';
}
