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
    test -z "''${FIGARO_ARIA:-}"
    test -z "''${FIGARO_TROUPE:-}"
    test "$1" = form
    test "$2" = apply
    test "$3" = --manifest
    manifest=$4
    shift 4
    check=false
    for arg in "$@"; do
      case "$arg" in
        --check) check=true ;;
        *) exit 3 ;;
      esac
    done
    printf 'check=%s\n' "$check"
    cat "$manifest"
  '';
  rolesModule = {
    programs.figaro = {
      package = fakeFigaro;
      outfits.fixture.charter.purpose = "Fixture purpose";
      roleApplicationRuntimeDir = "/fixture/roles";
      roleApplications = {
        preview = {
          roleId = "@11111111111111111111111111111111";
          outfits = [ "fixture" ];
          managed = [ "charter" ];
        };
        reviewed = {
          roleId = "@22222222222222222222222222222222";
          outfits = [ "fixture" ];
          managed = [ "charter" ];
          mode = "apply";
        };
      };
      applyRolesOnSwitch = true;
    };
  };
  roles = eval rolesModule;
  rolesAndRestart = eval {
    imports = [ rolesModule ];
    programs.figaro.restartOnSwitch = true;
  };
  shortId = builtins.tryEval (
    builtins.deepSeq
      (eval {
        programs.figaro.roleApplications.short = {
          roleId = "@1111111111";
          outfits = [ "fixture" ];
          managed = [ "charter" ];
        };
      }).programs.figaro.roleApplications
      true
  );
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
        roles.home.activation.figaroApplyRoles.after == [
          "linkGeneration"
          "figaroRestartOnSwitch"
        ]
        && !(roles.home.activation ? figaroRestartOnSwitch);
      why = "role application follows file installation without starting a restart owner";
    }
    {
      ok =
        rolesAndRestart.home.activation ? figaroRestartOnSwitch
        && builtins.elem "figaroRestartOnSwitch" rolesAndRestart.home.activation.figaroApplyRoles.after;
      why = "with both enabled, roles are applied after the restart so the new daemon answers";
    }
    {
      ok = !shortId.success;
      why = "a role id that is neither 8 nor 32 hex digits is refused at evaluation";
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
        head -n1 actual | grep -qx 'check=false'
        ${rolesPackage}/bin/figaro-apply-roles --check > checked
        head -n1 checked | grep -qx 'check=true'
        tail -n +2 actual > manifest.toml
        ${pkgs.remarshal}/bin/toml2json manifest.toml > manifest.json
        ${pkgs.jq}/bin/jq -e '
          .roles == {
            preview: {id: "@11111111111111111111111111111111", outfits: ["fixture"], managed: ["charter"], mode: "check"},
            reviewed: {id: "@22222222222222222222222222222222", outfits: ["fixture"], managed: ["charter"], mode: "apply"}
          }' manifest.json > /dev/null
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
