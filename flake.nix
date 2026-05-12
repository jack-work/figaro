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
          vendorHash = "sha256-UlKF37RGZPSkaLmOQNdQL9hjf6iOVxKt3dp2JfItPRg=";
          subPackages = [ "cmd/figaro" ];
          env.CGO_ENABLED = 0;

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
