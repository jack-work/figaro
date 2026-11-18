{ pkgs, self }:
pkgs.testers.runNixOSTest {
  name = "figaro-socket-activation";

  nodes.activated = {
    imports = [ self.nixosModules.default ];
    services.figaro = {
      enable = true;
      socketActivation = true;
      socketMode = "0660";
    };
    users.users.intruder = {
      isNormalUser = true;
      extraGroups = [ "figaro" ];
    };
  };

  nodes.bound = {
    imports = [ self.nixosModules.default ];
    services.figaro.enable = true;
  };

  testScript = ''
    import statistics

    def fig(node, cmd, user="figaro"):
        return node.succeed(f"runuser -u {user} -- env HOME=/var/lib/figaro figaro {cmd}")

    def ms(node, cmd):
        out = node.succeed(
            "s=$(date +%s%N); runuser -u figaro -- env HOME=/var/lib/figaro figaro "
            + cmd + " >/dev/null; e=$(date +%s%N); echo $(( (e - s) / 1000 ))"
        )
        return int(out.strip()) / 1000

    start_all()

    with subtest("the socket is up at boot and the angelus is not"):
        activated.wait_for_unit("figaro.socket")
        activated.succeed("test -S /run/figaro/angelus.sock")
        activated.fail("systemctl is-active figaro.service")
        print(activated.succeed("stat -c '%U %G %a' /run/figaro/angelus.sock"))
        inode = activated.succeed("stat -c %i /run/figaro/angelus.sock").strip()

    with subtest("the first call starts the angelus and is answered"):
        fig(activated, "ls --json")
        activated.succeed("systemctl is-active figaro.service")
        assert activated.succeed("stat -c %i /run/figaro/angelus.sock").strip() == inode, "the daemon rebound the socket"
        main_pid = activated.succeed("systemctl show -p MainPID --value figaro.service").strip()
        assert activated.succeed("cat /run/figaro/angelus.pid").strip() == main_pid, "the answer came from outside the unit"

    with subtest("with the service stopped the socket stays and the CLI still succeeds"):
        activated.succeed("systemctl stop figaro.service")
        activated.fail("systemctl is-active figaro.service")
        activated.succeed("systemctl is-active figaro.socket")
        activated.succeed("test -S /run/figaro/angelus.sock")
        out = fig(activated, "rest 2>&1")
        assert "not running" in out, out
        activated.succeed("sleep 0.5")
        assert activated.succeed("systemctl show -p ActiveState --value figaro.service").strip() == "inactive"
        fig(activated, "ls --json")
        activated.succeed("systemctl is-active figaro.service")
        assert activated.succeed("stat -c %i /run/figaro/angelus.sock").strip() == inode, "the socket was replaced"

    with subtest("the door keeps its admission: a uid that can reach the socket is still judged by the daemon"):
        status, out = activated.execute("runuser -u intruder -- figaro ls --json 2>&1")
        print(out)
        assert status != 0, out
        activated.succeed("systemctl is-active figaro.service")
        fig(activated, "ls --json")

    with subtest("the angelus still owns its runtime files beside the manager's socket"):
        activated.succeed("test -f /run/figaro/angelus.pid")
        print(activated.succeed("ls -la /run/figaro"))

    with subtest("without socket activation the daemon binds the socket itself, as before"):
        bound.wait_for_unit("figaro.service")
        bound.wait_until_succeeds("test -S /run/figaro/angelus.sock")
        bound.fail("systemctl cat figaro.socket")
        fig(bound, "ls --json")

    with subtest("start latency"):
        cold, warm, bound_cold = [], [], []
        for _ in range(5):
            activated.succeed("systemctl stop figaro.service && systemctl reset-failed figaro.service")
            cold.append(ms(activated, "ls --json"))
            warm.append(ms(activated, "ls --json"))
        for _ in range(5):
            bound.succeed("systemctl stop figaro.service && systemctl reset-failed figaro.service")
            out = bound.succeed(
                "s=$(date +%s%N); systemctl start figaro.service; "
                "until runuser -u figaro -- env HOME=/var/lib/figaro figaro ls --json >/dev/null 2>&1; do sleep 0.005; done; "
                "e=$(date +%s%N); echo $(( (e - s) / 1000 ))"
            )
            bound_cold.append(int(out.strip()) / 1000)
        def row(name, xs):
            print(f"KPI {name}: median {statistics.median(xs):.1f} ms, min {min(xs):.1f}, max {max(xs):.1f}, n {len(xs)}")
        row("activated first call, service stopped", cold)
        row("activated warm call", warm)
        row("bound: systemctl start to first answered call", bound_cold)
  '';
}
