{ pkgs, self }:
let
  probe = pkgs.writeText "house-probe" (builtins.concatStringsSep "; " [
    "echo uid=$(id -u)"
    "echo pwd=$(pwd)"
    "test -S /run/figaro/angelus.sock && echo DOOR-VISIBLE || echo door-absent"
    "{ echo from-tool > written; } 2>/dev/null && echo wrote || echo write-refused"
  ]);
  standIn = {
    users.users.toolbox = {
      isSystemUser = true;
      group = "toolbox";
      uid = 1500;
    };
    users.groups.toolbox = { };
    systemd.services.stand-in-box = {
      wantedBy = [ "multi-user.target" ];
      path = [
        pkgs.bash
        pkgs.coreutils
      ];
      environment.HOME = "/var/lib/stand-in";
      serviceConfig = {
        User = "toolbox";
        Group = "toolbox";
        RuntimeDirectory = "stand-in";
        RuntimeDirectoryMode = "0755";
        StateDirectory = "stand-in";
        UMask = "0000";
        ExecStart = "${self.packages.${pkgs.stdenv.hostPlatform.system}.figaro}/bin/figaro box serve --socket /run/stand-in/exec.sock --workspace /var/lib/stand-in";
      };
    };
  };
in
pkgs.testers.runNixOSTest {
  name = "figaro-house-mode";

  nodes.house = { ... }: {
    imports = [
      self.nixosModules.default
      standIn
    ];
    virtualisation.memorySize = 2048;
    services.figaro = {
      enable = true;
      owner = "alice";
      users = [ "carol" ];
      valet.users = [ "alice" ];
      providers.gateway.base_url = "http://127.0.0.1:8099/v1";
      settings.sandbox.boxes.stand-in.exec = "/run/stand-in/exec.sock";
      outfits.boxed.system = {
        sandbox = "stand-in";
        provider = "gateway";
        model = "stub";
      };
      outfits.bare.system = {
        provider = "gateway";
        model = "stub";
      };
      outfits.fanout.system.fanout.max_children = 3;
    };
    users.users.alice = {
      isNormalUser = true;
      uid = 1000;
    };
    users.users.carol = {
      isNormalUser = true;
      uid = 1002;
    };
    users.users.bob = {
      isNormalUser = true;
      uid = 1001;
    };
    systemd.services.stub-model = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 ${./stub_model.py} ${probe}";
    };
    environment.systemPackages = [ pkgs.jq ];
  };

  nodes.moving = { ... }: {
    imports = [
      self.nixosModules.default
      standIn
    ];
    virtualisation.memorySize = 2048;
    services.figaro = {
      enable = true;
      owner = "alice";
      importFrom = "/home/alice/.local/state/figaro";
      providers.gateway.base_url = "http://127.0.0.1:8099/v1";
      settings.sandbox.boxes.stand-in.exec = "/run/stand-in/exec.sock";
    };
    environment.etc."figaro-home/providers/gateway.toml".text = ''
      base_url = "http://127.0.0.1:8099/v1"
    '';
    # su from the test driver's root shell never reaches alice's user manager, so her
    # home daemon has no delegated cgroup for P4's tool attribution; the import
    # is what this node tests, so the home fixture leaves the peer check off.
    environment.etc."figaro-home/config.toml".text = ''
      [authz]
      peer_check = false
    '';
    environment.etc."figaro-home/outfits/home.toml".text = ''
      [system]
      provider = "gateway"
      model = "stub"
    '';
    users.users.alice = {
      isNormalUser = true;
      uid = 1000;
    };
    systemd.services.stub-model = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 ${./stub_model.py} ${probe}";
    };
  };

  testScript = ''
    import json

    def as_user(user, cmd):
        return house.succeed(f"su - {user} -c {json.dumps(cmd)}")

    def alice(cmd):
        return as_user("alice", cmd)

    def refused(user, cmd):
        status, out = house.execute(f"su - {user} -c {json.dumps(cmd)} 2>&1")
        print(out)
        assert status != 0, (user, cmd, out)
        return out

    def daemon_uid_refused(cmd):
        status, out = house.execute(f"runuser -u figaro -- env HOME=/var/lib/figaro figaro {cmd} 2>&1")
        print(out)
        assert status != 0, (cmd, out)
        return out

    def idle(fig_id):
        house.wait_until_succeeds(
            f"su - alice -c 'figaro status {fig_id} -j' | grep -Eq '\"state\": ?\"idle\"'", timeout=90)

    start_all()
    house.wait_for_unit("figaro.socket")
    house.wait_for_unit("stub-model.service")
    house.wait_for_unit("stand-in-box.service")
    house.wait_until_succeeds("test -S /run/stand-in/exec.sock")
    house.wait_for_unit("user@1000.service")
    house.wait_for_unit("figaro-valet.socket", "alice")

    with subtest("the door is the people's: figaro-door, 0660, in a directory they may cross"):
        print(house.succeed("stat -c '%U %G %a %n' /run/figaro /run/figaro/angelus.sock"))
        assert house.succeed("stat -c '%G %a' /run/figaro/angelus.sock").split() == ["figaro-door", "660"]
        house.succeed("id -nG alice | grep -qw figaro-door && id -nG carol | grep -qw figaro-door")
        house.fail("id -nG alice | tr ' ' '\\n' | grep -qx figaro")
        house.fail("systemctl is-active figaro.service")

    with subtest("the rendered config names the owner, the door users and no local tools"):
        env = house.succeed("systemctl show figaro.service -p Environment --value")
        config_dir = [kv.split("=", 1)[1] for kv in env.split() if kv.startswith("FIGARO_CONFIG_DIR=")][0]
        rendered = house.succeed(f"cat {config_dir}/config.toml")
        print(rendered)
        assert 'owner = "user:1000"' in rendered, rendered
        assert '1000 = "user:1000"' in rendered and '1002 = "user:1002"' in rendered, rendered
        assert "local_tools = false" in rendered, rendered
        assert 'default = "valet-alice"' in rendered, rendered
        assert 'figaro_claim = "required"' in rendered and "/run/figaro-valet/alice/exec.sock" in rendered, rendered

    with subtest("the owner's first call starts the angelus as uid figaro"):
        print(alice("figaro ls --json"))
        house.succeed("systemctl is-active figaro.service")
        assert house.succeed("ps -o user= -p $(systemctl show -p MainPID --value figaro.service)").strip() == "figaro"

    with subtest("a person outside the door, and the daemon's own uid, are refused"):
        refused("bob", "figaro ls --json")
        daemon_uid_refused("new -j -O boxed -- hello")
        refused("carol", "figaro new -j -O boxed -- hello")

    with subtest("the owner mints in a box and forks"):
        parent = json.loads(alice("figaro new -j -O boxed -- hello").strip().splitlines()[-1])["aria_id"]
        idle(parent)
        child = json.loads(alice(f"figaro fork {parent} -j -- again").strip().splitlines()[-1])["aria_id"]
        idle(child)
        state = json.loads(alice(f"figaro state show {child} -j"))
        assert state["system"]["sandbox"] == "stand-in", state
        assert child != parent
        shown = alice(f"figaro show {parent}")
        assert "uid=1500" in shown, shown

    with subtest("local is refused at birth"):
        out = refused("alice", "figaro new -j -O bare -S system.sandbox=local -- hello")
        assert "local_tools = false" in out, out

    with subtest("the valet's door is the daemon's, reachable by alice's valet alone"):
        print(house.succeed("stat -c '%U %G %a %n' /run/figaro/boxes /run/figaro/boxes/valet-alice /run/figaro/boxes/valet-alice/door.sock /run/figaro-valet/alice/exec.sock"))
        assert house.succeed("stat -c '%G %a' /run/figaro/boxes/valet-alice/door.sock").split() == ["figaro-valet-alice", "660"]
        house.fail("su - carol -c 'test -r /run/figaro/boxes/valet-alice/door.sock'")
        doc = json.loads(alice("figaro doctor mode -j"))
        print(doc)
        assert doc["mode"] == "house" and doc["valet"]["answers"] is True, doc

    with subtest("a figaro naming no sandbox runs in the owner's valet: its tool is alice, fenced"):
        alice("mkdir -p ~/dev/work && figaro valet grant '*' ~/dev/work rw --for 1h")
        fig = json.loads(alice("figaro new -j -O bare -S system.cwd=/home/alice/dev/work -- probe").strip().splitlines()[-1])["aria_id"]
        idle(fig)
        state = json.loads(alice(f"figaro state show {fig} -j"))
        assert state["system"]["sandbox"] == "valet-alice", state
        shown = alice(f"figaro show {fig}")
        print(shown)
        assert "uid=1000" in shown and "pwd=/home/alice/dev/work" in shown and "wrote" in shown, shown
        assert "door-absent" in shown and "DOOR-VISIBLE" not in shown, shown
        assert house.succeed("stat -c %U /home/alice/dev/work/written").strip() == "alice"

    with subtest("the owner binding: alice applies a managed fan-out, the daemon's uid cannot"):
        role = json.loads(alice(f"figaro form new -S name=house-role,target-aria={parent} -j"))["form_id"]
        daemon_uid_refused(f"form apply --id {role} -O fanout --managed system.fanout.max_children -j")
        print(alice(f"figaro form apply --id {role} -O fanout --managed system.fanout.max_children -j"))
        got = json.loads(alice(f"figaro state show {role} -j"))
        assert got["system"]["fanout"]["max_children"] == 3, got

    with subtest("a daemon that cannot start never fails its socket, and fixing the cause is enough"):
        house.succeed("systemctl stop figaro.service")
        house.succeed("chmod 000 /var/lib/figaro/arias")
        since = house.succeed("date +%s").strip()
        for _ in range(6):
            house.execute("su - alice -c 'timeout 5 figaro ls --json' >/dev/null 2>&1")
        starts = int(house.succeed(f"journalctl -u figaro.service --since @{since} -o cat | grep -c '^Starting figaro angelus' || true").strip())
        print(f"KPI crash loop: {starts} starts in 30 s of calls")
        house.succeed("systemctl is-active figaro.socket")
        assert house.succeed("systemctl show figaro.socket -p Result --value").strip() == "success"
        assert 0 < starts <= 15, starts
        house.succeed("chmod 700 /var/lib/figaro/arias")
        house.wait_until_succeeds("su - alice -c 'timeout 10 figaro ls --json'", timeout=60)
        house.succeed("systemctl is-active figaro.service")
    home_env = "env -u FIGARO_NO_SELF_SPAWN FIGARO_RUNTIME_DIR=/tmp/fighome FIGARO_CONFIG_DIR=/etc/figaro-home FIGARO_HUSH_APP=figaro-house-test FIGARO_HUSH_DIR=/tmp/fighush FIGARO_HUSH_PASSPHRASE=house-test"

    def moving_alice(cmd):
        return moving.succeed(f"su - alice -c {json.dumps(cmd)}")

    def moving_idle(fig_id, env=""):
        moving.wait_until_succeeds(
            f"su - alice -c '{env} figaro status {fig_id} -j' | grep -Eq '\"state\": ?\"idle\"'", timeout=90)

    with subtest("a home-mode store: alice's own daemon, her tools run as her"):
        moving.wait_for_unit("figaro.socket")
        moving.wait_for_unit("stub-model.service")
        moving.fail("test -e /var/lib/figaro/arias")
        home_fig = json.loads(moving_alice(f"{home_env} figaro new -j -O home -- hello").strip().splitlines()[-1])["aria_id"]
        moving_idle(home_fig, home_env)
        shown = moving_alice(f"{home_env} figaro show {home_fig}")
        assert "uid=1000" in shown, shown
        home_role = json.loads(moving_alice(f"{home_env} figaro form new -S name=moved-role,target-aria={home_fig} -j"))["form_id"]

    with subtest("while her home daemon holds the store, the house refuses to start and says why"):
        status, out = moving.execute("su - alice -c 'timeout 15 figaro ls --json' 2>&1")
        print(out)
        assert status != 0, out
        moving.wait_until_succeeds("journalctl -u figaro.service -o cat | grep -q 'a home-mode angelus holds /home/alice/.local/state/figaro'", timeout=30)
        moving.fail("test -e /var/lib/figaro/arias")
        moving.succeed("systemctl is-active figaro.socket")

    with subtest("after figaro rest the next start imports, and the house is hers"):
        moving_alice(f"{home_env} figaro rest")
        before = moving.succeed("find /home/alice/.local/state/figaro/arias -type f | sort | xargs sha256sum | sha256sum")
        listed = moving.wait_until_succeeds("su - alice -c 'timeout 20 figaro ls --json'", timeout=120)
        print(listed)
        assert home_fig in listed, listed
        print(moving.succeed("stat -c '%U %G %a %n' /var/lib/figaro /var/lib/figaro/arias /var/lib/figaro/imported-from"))
        assert moving.succeed("find /var/lib/figaro/arias ! -user figaro | wc -l").strip() == "0"
        assert moving.succeed("cat /var/lib/figaro/imported-from").startswith("/home/alice/.local/state/figaro ")
        after = moving.succeed("find /home/alice/.local/state/figaro/arias -type f | sort | xargs sha256sum | sha256sum")
        assert before == after, "the import changed the home store"
        state = json.loads(moving_alice(f"figaro state show {home_role} -j"))
        assert state["name"] == "moved-role", state

    with subtest("an imported figaro on local is refused until it names a box, then runs there"):
        status, out = moving.execute(f"su - alice -c 'figaro send --id {home_fig} -- again' 2>&1")
        print(out)
        assert status != 0 and "local_tools = false" in out, out
        moving_alice(f"figaro set --id {home_fig} system.sandbox stand-in")
        moving_alice(f"figaro send --id {home_fig} -f -- again")
        moving_idle(home_fig)
        shown = moving_alice(f"figaro show {home_fig}")
        assert "again" in shown and shown.count("done") >= 2, shown
        assert json.loads(moving_alice(f"figaro state show {home_fig} -j"))["system"]["sandbox"] == "stand-in"

    with subtest("a restart does not import again"):
        moving.succeed("systemctl restart figaro.service")
        moving.succeed("journalctl -u figaro.service -o cat | grep -c 'is now /var/lib/figaro/arias' | grep -qx 1")
  '';
}
