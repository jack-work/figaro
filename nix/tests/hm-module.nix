{ pkgs, self }:
let
  lib = pkgs.lib.extend (final: prev: {
    hm.dag.entryAfter = after: data: { inherit after data; };
  });
  stubs = {
    options.home.packages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = [ ];
    };
    options.home.activation = lib.mkOption {
      type = lib.types.attrsOf lib.types.anything;
      default = { };
    };
  };
  eval = extra: (lib.evalModules {
    specialArgs = { inherit pkgs; };
    modules = [
      stubs
      self.homeManagerModules.default
      { programs.figaro.enable = true; }
      extra
    ];
  }).config;
  off = eval { };
  on = eval { programs.figaro.restartOnSwitch = true; programs.figaro.restartWait = 7; };
  step = on.home.activation.figaroRestartOnSwitch;
  data = builtins.unsafeDiscardStringContext step.data;
  exe = builtins.unsafeDiscardStringContext "${self.packages.${pkgs.stdenv.hostPlatform.system}.figaro}/bin/figaro";
  checks = [
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
pkgs.runCommand "figaro-hm-module" { passAsFile = [ "script" ]; script = step.data; } (
  if failed == [ ] then ''
    cp $scriptPath $out
  '' else ''
    echo "${lib.concatMapStringsSep "\n" (c: "FAIL: " + c.why) failed}" >&2
    exit 1
  ''
)
