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
  nodes.lan = { lib, nodes, ... }: {
    networking.interfaces.eth1.ipv6.addresses = [ { address = "fd00:1::2"; prefixLength = 64; } ];
    networking.interfaces.eth1.ipv4.routes = [ { address = "10.233.1.0"; prefixLength = 24; via = nodes.host.networking.primaryIPAddress; } ];
    networking.defaultGateway6 = { address = "fd00:1::1"; interface = "eth1"; };
    networking.firewall.allowedUDPPorts = [ 9998 9999 ];
    systemd.services.v6-sink = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.socat}/bin/socat -u UDP6-RECV:9999,reuseaddr OPEN:/tmp/v6-arrived,creat,append";
    };
    systemd.services.v4-sink = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.socat}/bin/socat -u UDP4-RECV:9998,reuseaddr OPEN:/tmp/v4-arrived,creat,append";
    };
  };

  nodes.host = { config, ... }: {
    imports = [ self.nixosModules.container ];
    boot.kernel.sysctl."net.ipv6.conf.all.forwarding" = 1;
    networking.interfaces.eth1.ipv6.addresses = [ { address = "fd00:1::1"; prefixLength = 64; } ];
    virtualisation.memorySize = 2048;
    virtualisation.useNixStoreImage = true;
    services.figaro-container = {
      enable = true;
      externalInterface = "eth1";
      memoryMax = "1G";
      nameservers = [ "10.233.1.1" ];
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
    networking.firewall.interfaces."ve-figaro".allowedUDPPorts = [ 53 ];
    services.dnsmasq = {
      enable = true;
      resolveLocalQueries = true;
      settings = {
        listen-address = [ "127.0.0.1" "10.233.1.1" ];
        bind-dynamic = true;
        address = [ "/door.figaro.test/10.233.1.1" ];
      };
    };
    systemd.services.stub-model = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 ${./stub_model.py} ${probe}";
    };
    environment.systemPackages = [ self.packages.${pkgs.stdenv.hostPlatform.system}.figaro pkgs.jq pkgs.socat ];
  };

  testScript = { nodes, ... }: ''
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

    with subtest("nspawn neither waits on the guest's readiness nor links its journal into the host's"):
        argv = host.succeed("tr '\\0' '\\n' < /proc/$(systemctl show container@figaro -p MainPID --value)/cmdline").split("\n")
        for flag in ("--notify-ready", "--link-journal"):
            given = [a for a in argv if a.startswith(flag + "=")]
            assert given and given[-1] == flag + "=no", (flag, given)
        machine = host.succeed("machinectl show figaro -p Id --value").strip()
        host.fail(f"test -e /var/log/journal/{machine} || test -L /var/log/journal/{machine}")
        inside("journalctl -u figaro.service -n1 --no-pager")

    with subtest("the container's root is an unprivileged uid range on the host"):
        for m in ("uid_map", "gid_map"):
            assert inside(f"cat /proc/self/{m}").split() == ["0", "524288", "65536"], m
        leader = host.succeed("machinectl show figaro -p Leader --value").strip()
        assert host.succeed(f"stat -c %u /proc/{leader}").strip() == "524288"

    host_devs = set(host.succeed("findmnt -rn -o MAJ:MIN").split())

    def host_binds():
        top = {}
        for line in inside("findmnt -rn -o TARGET,MAJ:MIN,OPTIONS").splitlines():
            target, dev, opts = line.split(" ", 2)
            top[target] = (dev, "ro" if "ro" in opts.split(",") else "rw")
        return {t: mode for t, (dev, mode) in top.items() if dev in host_devs}

    with subtest("the container sees only the declared host mounts, read-only where it can be"):
        binds = host_binds()
        print(binds)
        # nspawn's --inaccessible lays the host's own mode-000 node over a
        # path, so those three show as read-only host mounts with nothing in them.
        assert binds == {
            "/": "rw",
            "/nix/store": "ro",
            "/nix/var/nix/profiles": "ro",
            "/nix/var/nix/gcroots": "ro",
            "/var/lib/figaro": "rw",
            "/run/figaro": "rw",
            "/run/host/incoming": "ro",
            "/run/host/os-release": "ro",
            "/sys/fs/cgroup": "rw",
            "/nix/var/nix/daemon-socket": "ro",
            "/nix/var/nix/db": "ro",
            "/run/host/unix-export": "ro",
        }, binds
        for gone in ("/nix/var/nix/daemon-socket", "/nix/var/nix/db", "/run/host/unix-export"):
            assert inside(f"stat -c %a {gone}").strip() == "0", gone
        inside("sh -c 'test ! -e /nix/var/nix/daemon-socket/socket'")

    with subtest("no host D-Bus or machined socket reaches the container"):
        socks = set(inside("find /run/figaro -type s").split())
        assert socks <= {"/run/figaro/angelus.sock", "/run/figaro/web.sock"}, socks
        assert host.succeed("find /run/systemd/nspawn/propagate/figaro -mindepth 1").strip() == ""
        bus = inside("findmnt -rn -o MAJ:MIN -T /run/dbus/system_bus_socket").strip()
        assert bus not in host_devs, bus
        assert inside("busctl --system get-property org.freedesktop.systemd1 /org/freedesktop/systemd1 "
                      "org.freedesktop.systemd1.Manager Virtualization").strip() == 's "systemd-nspawn"'
        inside("sh -c 'test ! -e /run/systemd/machine'")

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

    # On spain, the host's resolv.conf names its own dnsmasq at 127.0.0.1.
    # The containers module copies it over the container's at every start,
    # owned by host root, which the container's private user range shows as
    # nobody; after the first boot resolvconf could not take it back and the
    # container lost DNS.
    with subtest("the container keeps its own resolver across a restart"):
        def resolves():
            out = inside("getent hosts door.figaro.test")
            assert out.split()[0] == "10.233.1.1", out
            assert "127.0.0.1" not in inside("cat /etc/resolv.conf")
            assert inside("systemctl --failed --no-legend").strip() == "", inside("systemctl --failed --no-legend")

        resolves()
        host.succeed("systemctl restart container@figaro.service")
        host.wait_until_succeeds("nixos-container run figaro -- systemctl is-active figaro.service", timeout=120)
        host.wait_until_succeeds("nixos-container run figaro -- systemctl is-active resolvconf.service || "
                                 "nixos-container run figaro -- systemctl is-failed resolvconf.service", timeout=60)
        host.wait_until_succeeds("nixos-container run figaro -- getent hosts door.figaro.test", timeout=60)
        resolves()

    # The host forwards IPv6, as spain does, and the container's root can
    # give itself a global address and a default route via the host.
    import time
    lan.wait_for_unit("v6-sink.service")
    lan.wait_for_unit("v4-sink.service")
    lan_v4 = "${nodes.lan.networking.primaryIPAddress}"
    lan.wait_until_succeeds(f"ip -4 -o addr show dev eth1 | grep -qF ' {lan_v4}/'", timeout=60)
    host.succeed("systemd-run --unit=host-8081 -- ${pkgs.socat}/bin/socat TCP-LISTEN:8081,fork,reuseaddr SYSTEM:true")
    host.wait_until_succeeds("${pkgs.socat}/bin/socat -u OPEN:/dev/null TCP:127.0.0.1:8081")
    host.wait_until_succeeds("ping -6 -c1 -W1 fd00:1::2")
    gw = host.succeed("ip -6 -o addr show dev ve-figaro scope link | awk '{print $4}' | cut -d/ -f1").strip()

    def give_v6():
        inside("${pkgs.iproute2}/bin/ip -6 addr add fd99::2/64 dev eth0 nodad")
        inside(f"${pkgs.iproute2}/bin/ip -6 route add default via {gw} dev eth0")

    def reaches(addr, port):
        status, _ = host.execute(f"nixos-container run figaro -- ${pkgs.socat}/bin/socat -T3 -u OPEN:/dev/null TCP:{addr}:{port},connect-timeout=4")
        return status == 0

    # Arrival, not a connection: with the firewall stopped the host no
    # longer masquerades, and the LAN routes 10.233.1.0/24 back through the
    # host, so what the container sends lands either way.
    def forwards_v4(word):
        inside(f"sh -c 'echo {word} | ${pkgs.socat}/bin/socat -u - UDP4-SENDTO:{lan_v4}:9998'")
        time.sleep(3)
        return word in lan.succeed("cat /tmp/v4-arrived 2>/dev/null || true")

    def forwards_v6(word):
        inside(f"sh -c 'echo {word} | ${pkgs.socat}/bin/socat -u - UDP6-SENDTO:[fd00:1::2]:9999,bind=[fd99::2]'")
        time.sleep(3)
        return word in lan.succeed("cat /tmp/v6-arrived 2>/dev/null || true")

    def contained(word):
        assert not forwards_v4(word), "the container reached a blocked destination"
        assert not reaches("10.233.1.1", 8081), "the container reached a host port the firewall does not open to it"
        assert not forwards_v6(word), "the container forwarded IPv6 through the host"
        assert reaches("10.233.1.1", 8099), "the guard closed a port the firewall opens to the container"
        inside("getent hosts door.figaro.test")

    families = ("iptables -w -t mangle", "ip6tables -w -t mangle")

    def guarded():
        for fam in families:
            host.succeed(f"{fam} -C FORWARD -i ve-figaro -j figaro-guard", f"{fam} -C INPUT -i ve-figaro -j figaro-guard-in")

    def figaro_up():
        host.wait_until_succeeds("nixos-container run figaro -- systemctl is-active figaro.service", timeout=120)
        host.wait_until_succeeds("nixos-container run figaro -- getent hosts door.figaro.test", timeout=60)

    def state(unit):
        return host.succeed(f"systemctl show {unit} -p ActiveState --value").strip()

    with subtest("the guard alone contains the container, as built and with the reverse-path filter emptied"):
        give_v6()
        guarded()
        contained("as-built")
        host.succeed("for t in raw mangle; do ip6tables -t $t -F nixos-fw-rpfilter 2>/dev/null || true; done")
        contained("rpfilter-emptied")

    with subtest("the guard outlives a firewall restart and a stopped firewall"):
        host.succeed("systemctl restart firewall.service")
        guarded()
        contained("firewall-restarted")
        host.succeed("systemctl stop firewall.service")
        assert "nixos-fw" not in host.succeed("iptables -w -S INPUT; iptables -w -t mangle -S PREROUTING")
        guarded()
        contained("firewall-stopped")

    # tailscaled puts its own jumps first in filter INPUT and FORWARD
    # whenever it starts, and ts-forward accepts anything leaving by
    # tailscale0. The guard sits in mangle, ahead of every filter rule.
    with subtest("an accept that another service puts first in the filter table does not open the guard"):
        for fam in ("iptables -w", "ip6tables -w"):
            host.succeed(f"{fam} -I FORWARD 1 -j ACCEPT", f"{fam} -I INPUT 1 -j ACCEPT")
        contained("filter-accepts-first")
        for fam in ("iptables -w", "ip6tables -w"):
            host.succeed(f"{fam} -D FORWARD -j ACCEPT", f"{fam} -D INPUT -j ACCEPT")

    # With the firewall stopped, the guard is the only thing in the way, so
    # taking its jumps out must open every path the checks above found shut.
    with subtest("without the guard and the firewall, every one of those paths is open"):
        for fam in families:
            host.succeed(f"{fam} -D FORWARD -i ve-figaro -j figaro-guard", f"{fam} -D INPUT -i ve-figaro -j figaro-guard-in")
        assert forwards_v4("unguarded"), "no path to the blocked destination even unguarded"
        assert reaches("10.233.1.1", 8081), "no path to the host port even unguarded"
        assert forwards_v6("unguarded"), "no IPv6 path even unguarded"
        host.succeed("systemctl start firewall.service")

    with subtest("a restarted guard restarts the container behind it"):
        leader = host.succeed("machinectl show -p Leader --value figaro").strip()
        host.succeed("systemctl restart figaro-guard-net.service")
        figaro_up()
        assert host.succeed("machinectl show -p Leader --value figaro").strip() != leader
        guarded()
        give_v6()
        contained("guard-restarted")

    with subtest("a guard that stops without a job for the container still stops it"):
        host.succeed("systemctl stop --job-mode=ignore-dependencies figaro-guard-net.service")
        host.wait_until_succeeds("systemctl show container@figaro -p ActiveState --value | grep -Exq 'inactive|failed'", timeout=60)
        assert "figaro" not in host.succeed("machinectl list --no-legend")
        host.succeed("systemctl start container@figaro.service")
        figaro_up()
        guarded()

    with subtest("a guard that fails to come back leaves the container stopped"):
        host.succeed("mkdir -p /run/systemd/system/figaro-guard-net.service.d")
        host.succeed("printf '[Service]\\nExecStartPre=${pkgs.coreutils}/bin/false\\n' > /run/systemd/system/figaro-guard-net.service.d/fail.conf")
        host.succeed("systemctl daemon-reload")
        host.fail("systemctl restart figaro-guard-net.service")
        assert state("figaro-guard-net.service") == "failed", state("figaro-guard-net.service")
        assert state("container@figaro.service") in ("inactive", "failed"), state("container@figaro.service")
        assert "figaro" not in host.succeed("machinectl list --no-legend")
        host.fail("systemctl start container@figaro.service")
        assert "figaro" not in host.succeed("machinectl list --no-legend")
        host.succeed("rm -r /run/systemd/system/figaro-guard-net.service.d && systemctl daemon-reload && systemctl reset-failed")
        host.succeed("systemctl start container@figaro.service")
        figaro_up()
        guarded()
        give_v6()
        contained("guard-back")
  '';
}
