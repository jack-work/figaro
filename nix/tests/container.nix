{ pkgs, self }:
let
  probe = pkgs.writeText "probe-command" (builtins.concatStringsSep "; " [
    "echo pwd=$(pwd)"
    "echo uid=$(id -u)"
    "echo userns=$(readlink /proc/self/ns/user)"
    "test -e /run/credentials/figaro.service/stub && echo CRED-VISIBLE || echo cred-hidden"
    "cat /run/credentials/figaro.service/stub 2>/dev/null && echo CRED-READ || echo cred-unreadable"
    "test -e /run/secrets/host-only && echo HOST-SECRET-VISIBLE || echo host-secret-absent"
    "test -e /var/lib/host-only && echo HOST-VARLIB-VISIBLE || echo host-varlib-absent"
    "touch /var/lib/figaro/arias/escape 2>/dev/null && echo STORE-WRITABLE || echo store-not-writable"
    "a=$(grep -l -a -- --angelus /proc/[0-9]*/cmdline 2>/dev/null | head -1 | cut -d/ -f3); echo angelus-pid=$a"
    "ls /proc/$a/root/ >/dev/null 2>&1 && echo ANGELUS-ROOT-READABLE || echo angelus-root-closed"
    "cat /proc/$a/environ >/dev/null 2>&1 && echo ANGELUS-ENV-READABLE || echo angelus-env-closed"
    "echo inside > box-wrote-this"
  ]);
in
pkgs.testers.runNixOSTest {
  name = "figaro-container";
  nodes.host = { config, ... }: {
    imports = [ self.nixosModules.container ];
    virtualisation.memorySize = 2048;
    virtualisation.useNixStoreImage = true;
    services.figaro-container = {
      enable = true;
      externalInterface = "eth1";
      memoryMax = "1G";
      credentials.stub = "/run/secrets/figaro-stub";
      figaro = {
        services.figaro.providers.gateway.base_url = "http://10.233.1.1:8099/v1";
        systemd.tmpfiles.rules = [ "d /var/lib/figaro/work 0700 figaro figaro -" ];
      };
    };
    systemd.tmpfiles.rules = [
      "d /nix/var/nix/db 0755 root root -"
      "d /nix/var/nix/daemon-socket 0755 root root -"
      "d /run/secrets 0750 root root -"
      "f /run/secrets/figaro-stub 0400 root root - stub-credential"
      "f /run/secrets/host-only 0400 root root - host-secret"
      "f /var/lib/host-only 0400 root root - host-varlib"
    ];
    networking.firewall.interfaces."ve-figaro".allowedTCPPorts = [ 8099 ];
    systemd.services.stub-model = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 ${./stub_model.py} ${probe}";
    };
    environment.systemPackages = [ self.packages.${pkgs.stdenv.hostPlatform.system}.figaro pkgs.jq ];
  };

  testScript = ''
    import json

    host.wait_for_unit("container@figaro.service")
    host.wait_for_unit("stub-model.service")
    host.wait_until_succeeds("nixos-container run figaro -- systemctl is-active figaro.service", timeout=120)
    host.wait_until_succeeds("test -S /run/figaro-door/angelus.sock", timeout=60)

    def inside(cmd):
        return host.succeed(f"nixos-container run figaro -- {cmd}")

    def as_figaro(cmd):
        return inside(f"runuser -u figaro -- env HOME=/var/lib/figaro FIGARO_RUNTIME_DIR=/run/figaro FIGARO_NO_SELF_SPAWN=1 {cmd}")

    with subtest("the container is confined by its slice and its own user range"):
        assert host.succeed("systemctl show container@figaro -p Slice --value").strip() == "figaro.slice"
        assert host.succeed("systemctl show figaro.slice -p MemoryMax --value").strip() == str(1 << 30)
        assert host.succeed("systemctl show figaro.slice -p CPUWeight --value").strip() == "20"
        uid = host.succeed("stat -c %u /var/lib/figaro").strip()
        assert uid == str(524288 + 2001), uid
        pid = host.succeed("systemctl show container@figaro -p MainPID --value").strip()
        print(host.succeed(f"cat /proc/{pid}/task/*/children | head -1; grep . /proc/$(pgrep -f 'figaro --angelus' | head -1)/uid_map"))

    with subtest("the credential reaches the angelus and nothing else on the host does"):
        assert inside("cat /run/credentials/figaro.service/stub").strip() == "stub-credential"
        inside("sh -c 'test ! -e /run/secrets/host-only'")
        inside("sh -c 'test ! -e /var/lib/host-only'")

    with subtest("the door answers on the host side of the bind mount"):
        out = host.succeed("${pkgs.util-linux}/bin/setpriv --reuid=526289 --regid=526289 --clear-groups env HOME=/tmp FIGARO_RUNTIME_DIR=/run/figaro-door FIGARO_NO_SELF_SPAWN=1 figaro list -j")
        print(out)
        host.fail("env FIGARO_RUNTIME_DIR=/run/figaro-door FIGARO_NO_SELF_SPAWN=1 HOME=/tmp figaro list -j")

    with subtest("a tool call runs under bwrap inside the container"):
        out = as_figaro("sh -c 'cd /var/lib/figaro/work && exec figaro new -j -S system.provider=gateway -S system.model=stub -S system.sandbox=bwrap -S system.cwd=/var/lib/figaro/work -- probe'")
        print(out)
        aria = json.loads(out.strip().splitlines()[-1])["aria_id"]
        host.wait_until_succeeds(
            "nixos-container run figaro -- runuser -u figaro -- env HOME=/var/lib/figaro FIGARO_RUNTIME_DIR=/run/figaro "
            f"FIGARO_NO_SELF_SPAWN=1 figaro status {aria} -j | grep -Eq '\"state\": ?\"idle\"'", timeout=90)
        print(host.succeed("wc -l /tmp/stub-requests.jsonl"))
        page = json.loads(as_figaro(f"figaro show {aria} -j"))
        shown = "\n".join(n.get("output", "") for p in page["parts"] for n in p.get("nodes", []))
        print(shown)
        assert "uid=2001" in shown, "the tool did not run"
        assert "angelus-pid=" in shown and "angelus-pid=\\n" not in shown, "the probe could not find the angelus"
        for bad in ["CRED-VISIBLE", "CRED-READ", "HOST-SECRET-VISIBLE", "HOST-VARLIB-VISIBLE", "STORE-WRITABLE",
                    "ANGELUS-ROOT-READABLE", "ANGELUS-ENV-READABLE"]:
            assert bad not in shown, bad
        assert host.succeed("cat /var/lib/figaro/work/box-wrote-this").strip() == "inside"
  '';
}
