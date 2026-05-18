{
  description = "figaro";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs supportedSystems (system: f {
        pkgs = nixpkgs.legacyPackages.${system};
      });
    in
    {
      overlays.default = final: prev: {
        figaro = final.buildGoModule rec {
          pname = "figaro";
          version = "0.1.0";
          src = self;
          vendorHash = "sha256-N+CC2f8kk5sjfir2OCdlfgkY+haMNuzMAIrXpJZtRiM=";
          subPackages = [ "cmd/figaro" ];
          env.CGO_ENABLED = 0;

          nativeBuildInputs = [ final.installShellFiles ];

          # self.shortRev is a clean 7-char hash; dirtyShortRev appends
          # "-dirty". Strip the suffix so cli.commit gets the bare rev
          # and cli.commitDirty carries the dirty signal separately.
          rev =
            if self ? rev then builtins.substring 0 12 self.rev
            else if self ? dirtyRev then builtins.substring 0 12 self.dirtyRev
            else "unknown";
          dirty = if self ? dirtyRev then "true" else "";
          ldflags = [
            "-s" "-w"
            "-X github.com/jack-work/figaro/internal/credo.version=${rev}"
            "-X github.com/jack-work/figaro/internal/cli.commit=${rev}"
            "-X github.com/jack-work/figaro/internal/cli.commitDirty=${dirty}"
          ];

          # Multi-call shims used to live here (q/l/x symlinks); they
          # were moved to user shell aliases. See ~/.config/fish/config.fish.
          #
          # `fig` is different — it's a pure rename (no argv rewrite),
          # so we install it as a symlink next to figaro. main.go uses
          # filepath.Base(os.Args[0]) so help/usage/completion reflect
          # whichever name was invoked. Users installing via `go install`
          # can replicate this with: ln -s figaro $(go env GOPATH)/bin/fig
          #
          # Shell completions are generated from the freshly-built
          # binary and dropped into the standard nixpkgs autoload
          # paths ($out/share/{bash-completion,zsh/site-functions,
          # fish/vendor_completions.d}). Every nix rebuild produces a
          # matching script atomically with the binary — no more stale
          # on-disk completions after an upgrade. The canExecute guard
          # skips generation when cross-compiling (host can't run the
          # just-built binary); installShellCompletion is still safe to
          # call from cross builds, the scripts just won't be present.
          postInstall = ''
            ln -s figaro $out/bin/fig
          '' + final.lib.optionalString
            (final.stdenv.buildPlatform.canExecute final.stdenv.hostPlatform) ''
            installShellCompletion --cmd figaro \
              --bash <($out/bin/figaro completion bash) \
              --zsh  <($out/bin/figaro completion zsh)  \
              --fish <($out/bin/figaro completion fish)
            installShellCompletion --cmd fig \
              --bash <($out/bin/fig completion bash) \
              --zsh  <($out/bin/fig completion zsh)  \
              --fish <($out/bin/fig completion fish)
          '';

          meta.mainProgram = "figaro";
        };
      };

      packages = forAllSystems ({ pkgs }: rec {
        figaro = (import nixpkgs {
          inherit (pkgs) system;
          overlays = [ self.overlays.default ];
        }).figaro;
        default = figaro;
      });

      devShells = forAllSystems ({ pkgs }: {
        default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
          ] ++ [ self.packages.${pkgs.system}.default ];
        };
      });
    };
}
