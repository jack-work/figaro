{ pkgs, self }:
let
  home = "/home/alice";
  probe = pkgs.writeText "valet-probe" (builtins.concatStringsSep "; " [
    "echo uid=$(id -u)"
    "echo pwd=$(pwd)"
    "echo granted=$(cat ${home}/dev/granted/file)"
    "echo from-tool > ${home}/dev/granted/written && echo granted-writable"
    "cat ${home}/dev/ro/file >/dev/null && echo ro-readable"
    "{ echo x > ${home}/dev/ro/written; } 2>/dev/null && echo RO-WRITABLE || echo ro-readonly"
    "test -e ${home}/dev/private/file && echo PRIVATE-VISIBLE || echo private-absent"
    "test -e ${home}/.local/state/figaro-valet/grants.toml && echo TABLE-VISIBLE || echo table-absent"
    "test -e ${home}/dev/conf/settings && echo HIDDEN-VISIBLE || echo hidden-absent"
    "test -S /run/figaro/angelus.sock && echo DAEMON-VISIBLE || echo daemon-absent"
    "test -S /run/hush/cred.sock && echo HUSH-VISIBLE || echo hush-absent"
    "test -e /run/user/1000/systemd && echo RUNTIME-VISIBLE || echo runtime-absent"
    "test -S /nix/var/nix/daemon-socket/socket && echo NIXD-VISIBLE || echo nixd-absent"
    "echo procs=$(ls /proc | grep -c '^[0-9]')"
    "echo nnp=$(sed -n 's/^NoNewPrivs:[[:space:]]*//p' /proc/self/status)"
    "echo cgroup=$(cat /proc/self/cgroup)"
  ]);
in
pkgs.testers.runNixOSTest {
  name = "figaro-valet";

  nodes.machine = { ... }: {
    imports = [ self.nixosModules.default ];
    virtualisation.memorySize = 2048;
    services.figaro = {
      enable = true;
      socketActivation = true;
      socketMode = "0660";
      providers.gateway.base_url = "http://127.0.0.1:8099/v1";
      settings.sandbox.boxes.valet-alice.exec = "/run/figaro-valet/alice/exec.sock";
      outfits.valet.system.sandbox = "valet-alice";
    };
    services.figaro-valet = {
      users = [ "alice" ];
      hide = [ "%h/dev/conf" ];
    };
    users.users.alice = {
      isNormalUser = true;
      uid = 1000;
      linger = true;
      extraGroups = [ "figaro" ];
    };
    users.users.bob.isNormalUser = true;
    systemd.services.stub-model = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.python3}/bin/python3 ${./stub_model.py} ${probe}";
    };
    systemd.services.fake-hush = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        ExecStart = "${pkgs.socat}/bin/socat UNIX-LISTEN:/run/hush/cred.sock,mode=0666,fork SYSTEM:true";
        RuntimeDirectory = "hush";
        RuntimeDirectoryMode = "0755";
      };
    };
    environment.systemPackages = [ pkgs.jq pkgs.gawk ];
  };

  testScript = ''
    import json
    import shlex

    def alice(cmd):
        return machine.succeed(f"su - alice -c {json.dumps(cmd)}")

    def as_figaro(cmd):
        return machine.succeed(f"runuser -u figaro -- env HOME=/var/lib/figaro {cmd}")

    def user_systemctl(args):
        return machine.succeed(f"systemctl --user --machine=alice@.host {args}")

    def call(name):
        out = as_figaro(f"figaro new -j -O valet -S system.provider=gateway -S system.model=stub -S system.cwd=${home}/dev/granted -- {shlex.quote(name)}")
        fig_id = json.loads(out.strip().splitlines()[-1])["aria_id"]
        machine.wait_until_succeeds(
            f"runuser -u figaro -- env HOME=/var/lib/figaro figaro status {fig_id} -j | grep -Eq '\"state\": ?\"idle\"'", timeout=90)
        page = json.loads(as_figaro(f"figaro show {fig_id} -j"))
        shown = "\n".join(n.get("output", "") for p in page["parts"] for n in p.get("nodes", []))
        print(shown)
        return fig_id, shown

    def lines(shown):
        return dict(l.split("=", 1) for l in shown.splitlines() if "=" in l)

    start_all()
    machine.wait_for_unit("figaro.socket")
    machine.wait_for_unit("stub-model.service")
    machine.wait_for_unit("fake-hush.service")
    machine.wait_for_unit("user@1000.service")
    machine.wait_for_unit("figaro-valet.socket", "alice")
    machine.wait_until_succeeds("test -S /run/hush/cred.sock")

    with subtest("the valet's socket is alice's, reachable by the figaro group and no one else"):
        print(machine.succeed("stat -c '%U %G %a %n' /run/figaro-valet/alice /run/figaro-valet/alice/exec.sock"))
        machine.fail("su - bob -c 'test -S /run/figaro-valet/alice/exec.sock'")

    with subtest("doctor mode reports house, userns and the live valet"):
        doc = json.loads(alice("FIGARO_RUNTIME_DIR=/run/figaro FIGARO_NO_SELF_SPAWN=1 figaro doctor mode -j"))
        print(doc)
        assert doc["mode"] == "house", doc
        assert doc["userns"] is True, doc
        assert doc["valet"]["answers"] is True, doc

    with subtest("outside the fence alice reaches the daemon, hush and her own runtime directory"):
        alice("test -S /run/figaro/angelus.sock && test -S /run/hush/cred.sock && test -e /run/user/1000/systemd")
        alice("mkdir -p ~/dev/granted ~/dev/conf ~/dev/ro ~/dev/private && echo g > ~/dev/granted/file && echo r > ~/dev/ro/file && echo p > ~/dev/private/file && echo s > ~/dev/conf/settings")

    with subtest("alice grants working trees from her own shell"):
        print(alice("figaro valet grant '*' ~/dev/granted rw --for 1h"))
        print(alice("figaro valet grant '*' ~/dev/ro ro --for 1h"))
        print(alice("figaro valet grant '*' ~/dev/conf rw --for 1h"))
        print(alice("figaro valet grants"))
        machine.succeed("test \"$(stat -c %a ${home}/.local/state/figaro-valet/grants.toml)\" = 600")

    with subtest("a peer that is not the figaro user is refused by the valet itself"):
        status, out = machine.execute("su - alice -c 'figaro box exec --socket /run/figaro-valet/alice/exec.sock --scope 3006ff4b42cafe -- id' 2>&1")
        print(out)
        assert status != 0 and "may not call this valet" in out, out

    with subtest("a figaro's tool runs as alice, inside the fence, in its own unit"):
        _, shown = call("probe")
        got = lines(shown)
        assert got.get("uid") == "1000", got
        assert got.get("pwd") == "${home}/dev/granted", got
        assert got.get("granted") == "g", got
        assert "granted-writable" in shown, shown
        assert "ro-readable" in shown and "ro-readonly" in shown and "RO-WRITABLE" not in shown, shown
        assert machine.succeed("cat ${home}/dev/granted/written").strip() == "from-tool"
        assert machine.succeed("stat -c %U ${home}/dev/granted/written").strip() == "alice"
        for gone in ["private", "table", "hidden", "daemon", "hush", "runtime", "nixd"]:
            assert f"{gone}-absent" in shown, (gone, shown)
            assert f"{gone.upper()}-VISIBLE" not in shown, (gone, shown)
        assert int(got["procs"]) < 10, got
        assert got.get("nnp") == "1", got
        assert "/figaro-valet@" in got["cgroup"] and "app.slice" in got["cgroup"], got["cgroup"]

    with subtest("a symlink a tool plants over a nested grant redirects nothing on the next call"):
        alice("mkdir -p ~/dev/granted/a/b && echo inner > ~/dev/granted/a/b/file")
        alice("figaro valet grant '*' ~/dev/granted/a/b rw --for 1h")
        _, shown = call("STUB-RUN<<cat a/b/file; mv a a2 && mkdir a && ln -s ../.. a/b && echo planted>>")
        assert "inner" in shown and "planted" in shown, shown
        _, shown = call("STUB-RUN<<test -e ${home}/dev/private/file && echo PRIVATE-VISIBLE || echo private-absent; "
                        "{ echo pwned > ${home}/dev/private/planted; } 2>/dev/null && echo PRIVATE-WRITABLE || echo private-unwritable>>")
        assert "private-absent" in shown and "private-unwritable" in shown, shown
        machine.fail("test -e ${home}/dev/private/planted")
        alice("rm ~/dev/granted/a/b && rmdir ~/dev/granted/a && mv ~/dev/granted/a2 ~/dev/granted/a")

    with subtest("memory per call"):
        units = user_systemctl("list-units --state=active --no-legend --plain 'figaro-valet@*.service'").split()
        print(units)
        unit = [u for u in units if u.startswith("figaro-valet@")][0]
        pid = user_systemctl(f"show -p MainPID --value {unit}").strip()
        raw_cur = user_systemctl(f"show -p MemoryCurrent --value {unit}").strip()
        cur_str = f"{int(raw_cur) / 1048576:.1f} MiB" if raw_cur.isdigit() else raw_cur
        cg = user_systemctl(f"show -p ControlGroup --value {unit}").strip()
        procs = machine.succeed(f"cat {json.dumps(f'/sys/fs/cgroup{cg}/cgroup.procs')}").split()
        proc_status = machine.succeed(f"grep -E 'VmRSS|RssAnon|RssFile' /proc/{pid}/status")
        rollup = machine.succeed(f"grep -E '^(Rss|Pss|Private_Clean|Private_Dirty|Shared_Clean)' /proc/{pid}/smaps_rollup")
        print(proc_status, rollup)
        rss = int([l.split()[1] for l in proc_status.splitlines() if l.startswith("VmRSS")][0])
        anon = int([l.split()[1] for l in proc_status.splitlines() if l.startswith("RssAnon")][0])
        pss = int([l.split()[1] for l in rollup.splitlines() if l.startswith("Pss:")][0])
        private = sum(int(l.split()[1]) for l in rollup.splitlines() if l.startswith("Private_"))
        shared = int([l.split()[1] for l in rollup.splitlines() if l.startswith("Shared_Clean:")][0])
        print(f"KPI valet per call: unit MemoryCurrent {cur_str}, processes {len(procs)}, VmRSS {rss / 1024:.2f} MiB, Pss {pss / 1024:.2f} MiB, RssAnon {anon / 1024:.2f} MiB, Private {private / 1024:.2f} MiB, Shared_Clean {shared / 1024:.2f} MiB")
        assert len(procs) == 1, procs
        assert private > 0, rollup

    with subtest("the kill switch: stopping alice's valet socket stops the running calls and cuts the daemon off"):
        assert user_systemctl("list-units --state=active --no-legend --plain 'figaro-valet@*.service'").strip(), "no live call to stop"
        user_systemctl("stop figaro-valet.socket")
        machine.wait_until_succeeds("test -z \"$(systemctl --user --machine=alice@.host list-units --state=active --no-legend --plain 'figaro-valet@*.service')\"", timeout=15)
        machine.fail("test -e /run/figaro-valet/alice/exec.sock")
        doc_off = json.loads(alice("FIGARO_RUNTIME_DIR=/run/figaro FIGARO_NO_SELF_SPAWN=1 figaro doctor mode -j"))
        assert doc_off["valet"]["answers"] is False, doc_off
        status, out = machine.execute(
            "runuser -u figaro -- env HOME=/var/lib/figaro "
            "figaro new -j -O valet -S system.provider=gateway -S system.model=stub -S system.cwd=${home}/dev/granted -- cut-off 2>&1"
        )
        print(out)
        assert status != 0 and "valet-alice" in out, (status, out)

    with subtest("the socket comes back for the next call"):
        user_systemctl("start figaro-valet.socket")
        _, shown = call("again")
        assert lines(shown).get("uid") == "1000", shown
  '';
}
