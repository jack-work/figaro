{ pkgs, self }:
let
  lib = pkgs.lib.extend (
    final: prev: {
      hm.dag.entryAfter = after: data: { inherit after data; };
    }
  );
  stubs = {
    options.assertions = lib.mkOption {
      type = lib.types.listOf lib.types.anything;
      default = [ ];
    };
    options.xdg.configFile = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
    };
    options.home.packages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
    };
    options.home.activation = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
    };
  };
  eval =
    extra:
    (lib.evalModules {
      specialArgs = { inherit pkgs; };
      modules = [
        stubs
        self.homeManagerModules.default
        { programs.figaro.enable = true; }
        extra
      ];
    }).config;
  off = eval { };
  on = eval {
    programs.figaro.restartOnSwitch = true;
    programs.figaro.restartWait = 7;
  };
  step = on.home.activation.figaroRestartOnSwitch;
  data = builtins.unsafeDiscardStringContext step.data;
  exe = builtins.unsafeDiscardStringContext "${
    self.packages.${pkgs.stdenv.hostPlatform.system}.figaro
  }/bin/figaro";
  fakeFigaro = pkgs.writeShellScriptBin "figaro" ''
    set -euo pipefail
    test "''${FIGARO_RUNTIME_DIR:-}" = /fixture/roles
    test "''${FIGARO_CONFIG_DIR:-}" = ${pkgs.emptyDirectory}
    test "''${FIGARO_NO_SELF_SPAWN:-}" = 1
    test "''${FIGARO_ARIA:-}" = fixture-caller
    test -z "''${FIGARO_TROUPE:-}"
    test "$1" = form
    test "$2" = apply
    shift 2
    id=
    check=false
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --id) id=$2; shift ;;
        --check) check=true ;;
      esac
      shift
    done
    test -n "$id"
    printf '%s %s\n' "$id" "$check"
  '';
  roles = eval {
    programs.figaro = {
      package = fakeFigaro;
      outfits.fixture.charter.purpose = "Fixture purpose";
      roleApplicationRuntimeDir = "/fixture/roles";
      roleApplications = {
        preview = {
          roleId = "@11111111";
          outfits = [ "fixture" ];
          managed = [ "charter" ];
        };
        reviewed = {
          roleId = "@22222222";
          outfits = [ "fixture" ];
          managed = [ "charter" ];
          mode = "apply";
        };
      };
      applyRolesOnSwitch = true;
    };
  };
  rolesPackage = lib.findFirst (p: (p.name or "") == "figaro-apply-roles") null roles.home.packages;
  checks = [
    {
      ok = off.programs.figaro.roleApplications == { } && !(off.home.activation ? figaroApplyRoles);
      why = "existing consumers get no role application by default";
    }
    {
      ok = roles.programs.figaro.roleApplications.preview.mode == "check";
      why = "declaring a role defaults to check, not apply";
    }
    {
      ok = rolesPackage != null && roles.xdg.configFile ? "figaro/outfits/fixture.toml";
      why = "declared outfits and their application command are installed";
    }
    {
      ok =
        roles.home.activation.figaroApplyRoles.after == [ "linkGeneration" ]
        && !(roles.home.activation ? figaroRestartOnSwitch);
      why = "role application follows file installation without starting a restart owner";
    }
    {
      ok = lib.all (a: a.assertion) roles.assertions;
      why = "the declared role application satisfies the module's assertions";
    }
    {
      ok = !(off.home.activation ? figaroRestartOnSwitch);
      why = "restartOnSwitch is off by default and then renders no activation step";
    }
    {
      ok = on.home.activation ? figaroRestartOnSwitch;
      why = "restartOnSwitch = true renders the activation step";
    }
    {
      ok = step.after == [ "linkGeneration" ];
      why = "the step runs after linkGeneration";
    }
    {
      ok = lib.hasInfix "${exe} upgrade --to ${exe} --if-changed --wait 7" data;
      why = "the step runs the NEW package by store path, only if the build changed, with restartWait";
    }
    {
      ok = !(lib.hasInfix "nix profile" data);
      why = "the step never looks a profile up";
    }
    {
      ok = lib.hasInfix "|| echo" data && lib.hasInfix "-n || true" data;
      why = "neither a real nor a dry activation can fail the switch";
    }
    {
      ok = builtins.elem self.packages.${pkgs.stdenv.hostPlatform.system}.figaro off.home.packages;
      why = "enable installs the package";
    }
  ];
  failed = builtins.filter (c: !c.ok) checks;
in
pkgs.runCommand "figaro-hm-module"
  {
    passAsFile = [ "script" ];
    script = data;
  }
  (
    if failed == [ ] then
      ''
        export FIGARO_ARIA=fixture-caller
        export FIGARO_TROUPE=must-not-be-forwarded
        ${rolesPackage}/bin/figaro-apply-roles > actual
        printf '%s\n' '@11111111 true' '@22222222 false' > expected
        diff -u expected actual
        ${rolesPackage}/bin/figaro-apply-roles --check > checked
        printf '%s\n' '@11111111 true' '@22222222 true' > expected-check
        diff -u expected-check checked
        ${rolesPackage}/bin/figaro-apply-roles --help > help
        grep -q '^Usage:' help
        if ${rolesPackage}/bin/figaro-apply-roles unexpected > unexpected 2>/dev/null; then
          echo 'unexpected application arguments were accepted' >&2
          exit 1
        fi
        cp $scriptPath $out
      ''
    else
      ''
        echo "${lib.concatMapStringsSep "\n" (c: "FAIL: " + c.why) failed}" >&2
        exit 1
      ''
  )
