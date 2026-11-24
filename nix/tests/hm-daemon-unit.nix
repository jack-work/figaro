{ pkgs, self }:
let
  figaro = self.packages.${pkgs.stdenv.hostPlatform.system}.figaro;
in
pkgs.testers.runNixOSTest {
  name = "figaro-hm-daemon-unit";

  nodes.machine = {
    imports = [ "${pkgs.home-manager.src}/nixos" ];
    users.users.alice = {
      isNormalUser = true;
      uid = 1000;
      linger = true;
    };
    home-manager.useGlobalPkgs = true;
    home-manager.useUserPackages = true;
    home-manager.users.alice = {
      imports = [ self.homeManagerModules.default ];
      home.stateVersion = "24.11";
      programs.bash.enable = true;
      programs.figaro = {
        enable = true;
        daemon = {
          enable = true;
          path = [ pkgs.coreutils ];
          environment.FIGARO_TEST_MARK = "from the unit";
          sshAuthSock = "%t/fleet-ssh-agent.sock";
        };
      };
    };
  };

  testScript = ''
    import json
    import shlex

    def alice(cmd):
        return machine.succeed("su -l alice -c " + shlex.quote("export XDG_RUNTIME_DIR=/run/user/1000; " + cmd))

    def alice_fails(cmd):
        status, out = machine.execute("su -l alice -c " + shlex.quote("export XDG_RUNTIME_DIR=/run/user/1000; " + cmd + " 2>&1"))
        assert status != 0, out
        return out

    def main_pid():
        return int(alice("systemctl --user show -p MainPID --value figaro.service").strip())

    def active(unit):
        try:
            machine.wait_until_succeeds("su -l alice -c " + shlex.quote("export XDG_RUNTIME_DIR=/run/user/1000; systemctl --user is-active " + unit), timeout=1800)
        except Exception:
            print(machine.execute("su -l alice -c " + shlex.quote("export XDG_RUNTIME_DIR=/run/user/1000; systemctl --user status figaro.socket figaro.service; systemctl --user list-jobs; systemctl --user list-units --failed 2>&1"))[1])
            raise

    def serving():
        alice("figaro ls --json >/dev/null")

    machine.start()
    machine.wait_for_unit("multi-user.target")
    machine.wait_for_unit("home-manager-alice.service")

    with subtest("the user manager starts the unit and holds the door in its socket"):
        active("figaro.socket")
        active("figaro.service")
        machine.succeed("test -S /run/user/1000/figaro/angelus.sock")
        assert alice("echo $FIGARO_NO_SELF_SPAWN").strip() == "1"

    with subtest("the daemon runs with the declared environment and none of a CLI's"):
        pid = main_pid()
        environ = machine.succeed(f"tr '\\0' '\\n' < /proc/{pid}/environ")
        print(environ)
        assert "FIGARO_NO_SELF_SPAWN=1" in environ
        assert "FIGARO_RUNTIME_DIR=/run/user/1000/figaro" in environ
        assert "FIGARO_TEST_MARK=from the unit" in environ
        assert "SSH_AUTH_SOCK=/run/user/1000/fleet-ssh-agent.sock" in environ
        path = [l for l in environ.splitlines() if l.startswith("PATH=")][0]
        assert path.startswith("PATH=${pkgs.coreutils}/bin:/home/alice/.nix-profile/bin:/home/alice/.local/bin"), path

    with subtest("a CLI under FIGARO_NO_SELF_SPAWN connects to the unit's daemon"):
        serving()
        doc = json.loads(alice("figaro doctor unit -j"))
        assert doc["ok"], doc
        assert doc["unit"]["main_pid"] == main_pid(), doc
        machine.fail("pgrep -u alice -f figaro-angelus-")

    with subtest("kill -9 and the unit brings the angelus back"):
        before = main_pid()
        machine.succeed(f"kill -9 {before}")
        machine.wait_until_succeeds(
            "su -l alice -c 'export XDG_RUNTIME_DIR=/run/user/1000; test $(systemctl --user show -p MainPID --value figaro.service) -ne " + str(before) + "'",
            timeout=60,
        )
        active("figaro.service")
        machine.wait_until_succeeds("su -l alice -c 'export XDG_RUNTIME_DIR=/run/user/1000; figaro ls --json >/dev/null'", timeout=60)
        assert main_pid() != before

    with subtest("the socket outlives the daemon, and the first call starts it"):
        alice("systemctl --user stop figaro.service")
        alice_fails("systemctl --user is-active figaro.service")
        alice("systemctl --user is-active figaro.socket")
        serving()
        alice("systemctl --user is-active figaro.service")

    with subtest("with the socket gone too a CLI starts nothing"):
        alice("systemctl --user stop figaro.socket figaro.service")
        out = alice_fails("figaro ls --json")
        print(out)
        alice_fails("systemctl --user is-active figaro.service")
        machine.fail("pgrep -u alice -f -- '--angelus'")
        alice("systemctl --user start figaro.socket")
        serving()

    with subtest("upgrade restarts the unit under FIGARO_NO_SELF_SPAWN and confirms it serves"):
        before = main_pid()
        out = alice("figaro upgrade --to ${figaro}/bin/figaro --wait 20 2>&1")
        print(out)
        assert main_pid() != before
        serving()
        assert "is running" in out, out
  '';
}
