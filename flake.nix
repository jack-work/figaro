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

        # `nix develop .#swap` enters a shell that swaps the user's
        # installed figaro for this worktree's build. The previous
        # profile entry is captured on entry and restored on exit, so
        # the shell behaves like a temporary "try this version" gate.
        #
        # On entry:
        #   1. Stop the running daemon with --keep-pids so PID
        #      bindings get persisted to disk and any next `fig`
        #      invocation can restore them.
        #   2. Find the current figaro entry in the user's nix
        #      profile (by storePath match against "figaro-VERSION").
        #      Capture its key and originalUrl.
        #   3. Remove the captured entry.
        #   4. Install this worktree's freshly-built figaro by store
        #      path.
        #
        # On exit (bash EXIT trap):
        #   1. Stop the worktree daemon with --keep-pids (bindings
        #      survive the swap-back).
        #   2. Remove the worktree entry from the profile.
        #   3. Reinstall the captured original entry, preferring its
        #      originalUrl so the user's flake-tracking is preserved
        #      (e.g. `git+file:...?ref=main`). Fall back to the store
        #      path if no URL was captured.
        #
        # The store path of the worktree build is passed in via the
        # FIGARO_WORKTREE_PATH env var so Nix builds the package
        # before the shellHook runs (the path interpolation forces
        # evaluation). It's also added to buildInputs so the binary
        # is on the in-shell PATH directly, independent of profile
        # state — defensive against a partial enter-failure.
        #
        # Edge cases:
        #   - No existing figaro in profile: skip save/restore; just
        #     install + remove the worktree entry.
        #   - jq absent: nothing parses; abort early.
        #   - SIGKILL of the shell: the EXIT trap doesn't fire,
        #     profile stays on the worktree entry. Recover with a
        #     fresh `nix develop .#swap` + exit.
        swap = pkgs.mkShell {
          name = "figaro-swap";
          buildInputs = with pkgs; [
            go gopls gotools
            jq          # JSON parse of `nix profile list --json`
            nix         # explicit so the version in the shell matches
          ] ++ [ self.packages.${pkgs.system}.default ];

          FIGARO_WORKTREE_PATH = "${self.packages.${pkgs.system}.default}";

          shellHook = ''
            set -u

            _figaro_swap_find_entry() {
              nix profile list --json 2>/dev/null | jq -r '
                .elements | to_entries[]
                | select(.value.storePaths[]? | test("/[a-z0-9]+-figaro-[0-9]+(\\.[0-9]+)*$"))
                | .key
              ' | head -1
            }

            _figaro_swap_stop_daemon() {
              # Use whichever `fig` is on PATH at the moment; bindings
              # get written to disk regardless of which version it is.
              if command -v fig >/dev/null 2>&1; then
                fig stop --keep-pids >/dev/null 2>&1 || true
              fi
            }

            _figaro_swap_enter() {
              if ! command -v jq >/dev/null 2>&1; then
                echo "[figaro-swap] jq missing; aborting" >&2
                return 1
              fi

              local entry old_path old_url
              entry=$(_figaro_swap_find_entry || true)
              if [ -n "$entry" ]; then
                old_path=$(nix profile list --json \
                  | jq -r --arg k "$entry" '.elements[$k].storePaths[0]')
                old_url=$(nix profile list --json \
                  | jq -r --arg k "$entry" '.elements[$k].originalUrl // empty')
                export FIGARO_SWAP_OLD_KEY="$entry"
                export FIGARO_SWAP_OLD_PATH="$old_path"
                export FIGARO_SWAP_OLD_URL="$old_url"
                echo "[figaro-swap] saving profile entry:" >&2
                echo "  key:  $entry" >&2
                echo "  path: $old_path" >&2
                [ -n "$old_url" ] && echo "  url:  $old_url" >&2
              else
                echo "[figaro-swap] no figaro in profile; nothing to restore on exit" >&2
              fi

              _figaro_swap_stop_daemon

              if [ -n "''${FIGARO_SWAP_OLD_KEY:-}" ]; then
                nix profile remove "$FIGARO_SWAP_OLD_KEY" >&2
              fi
              nix profile install "$FIGARO_WORKTREE_PATH"
              echo "[figaro-swap] worktree figaro installed: $FIGARO_WORKTREE_PATH" >&2
            }

            _figaro_swap_exit() {
              echo "[figaro-swap] exit — restoring previous figaro" >&2
              _figaro_swap_stop_daemon

              # The worktree entry will be keyed by its derivation name
              # since we installed it by store path. Re-find it now in
              # case other profile churn happened.
              local current
              current=$(nix profile list --json 2>/dev/null \
                | jq -r --arg p "$FIGARO_WORKTREE_PATH" '
                    .elements | to_entries[]
                    | select(.value.storePaths[]? == $p)
                    | .key' | head -1)
              if [ -n "$current" ]; then
                nix profile remove "$current" >&2 || true
              fi

              if [ -n "''${FIGARO_SWAP_OLD_URL:-}" ]; then
                nix profile install "$FIGARO_SWAP_OLD_URL" >&2
                echo "[figaro-swap] restored via flake url: $FIGARO_SWAP_OLD_URL" >&2
              elif [ -n "''${FIGARO_SWAP_OLD_PATH:-}" ]; then
                nix profile install "$FIGARO_SWAP_OLD_PATH" >&2
                echo "[figaro-swap] restored via store path: $FIGARO_SWAP_OLD_PATH" >&2
              else
                echo "[figaro-swap] nothing captured to restore" >&2
              fi
            }

            _figaro_swap_enter
            trap _figaro_swap_exit EXIT

            echo "[figaro-swap] active — exit shell to restore the previous figaro" >&2
          '';
        };
      });
    };
}
