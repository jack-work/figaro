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
          vendorHash = "sha256-dG85TQIcEuVdv7lbC/PfTpaacOpagfI8qMz5IOd6b9s=";
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

            # First-party skills ship alongside the binary at
            # $out/share/figaro/skills; outfit's loader merges them under any
            # `dirName = "skills"` table (user config overrides by name). See
            # bundledSkillsRoot (<exe>/../share/figaro).
            if [ -d "$src/skills" ]; then
              mkdir -p $out/share/figaro
              cp -r $src/skills $out/share/figaro/skills
            fi
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

      devShells = forAllSystems ({ pkgs }: let
        figaroPkg = self.packages.${pkgs.system}.default;

        # Each override knob is an attribute on a profile. A null
        # value means "inherit from the real user environment" — the
        # corresponding env var is left untouched. A string means
        # "set this env var"; "@dev" is a sentinel expanded to a
        # dev-scoped path under $FIGARO_DEV_ROOT/<knob>.
        #
        # The dev-shell knobs are:
        #
        #   runtime  → FIGARO_RUNTIME_DIR  (socket, bindings, PID)
        #   config   → FIGARO_CONFIG_DIR   (loadouts, providers, config.toml)
        #   state    → FIGARO_STATE_DIR    (arias, OTel data)
        #   hush     → FIGARO_HUSH_APP     (hush identity + socket)
        #
        # All four default to "@dev" in the helper, so the helper
        # caller only overrides what they want to share. The named
        # presets at the bottom show common compositions.
        #
        # Note: hush's @dev sentinel resolves to a string AppName
        # like "figaro-dev-<profile>", not a path, because hush
        # derives its own dirs from AppName internally. To share the
        # global hush identity, set hush = null.
        mkFigaroShell = {
          name,
          runtime ? "@dev",
          config  ? "@dev",
          state   ? "@dev",
          hush    ? "@dev",
        }: let
          # Translate a knob value into a shell snippet that either
          # exports the env var or leaves it inherited. "@dev" is
          # expanded to a path under $FIGARO_DEV_ROOT.
          #
          # All branches use `: "''${VAR:=...}"` instead of `export
          # VAR=...` so a pre-set env var from the caller's shell
          # wins. This makes the presets composable from outside:
          # `FIGARO_HUSH_APP=figaro nix develop .#clean` will keep
          # the caller's value rather than overwriting it.
          mkKnob = envVar: subdir: value:
            if value == null then ''
              # ${envVar}: inheriting real user environment
            ''
            else if value == "@dev" then ''
              : "''${${envVar}:=$FIGARO_DEV_ROOT/${subdir}}"
              export ${envVar}
              mkdir -p "''${${envVar}}"
            ''
            else ''
              : "''${${envVar}:=${value}}"
              export ${envVar}
            '';
          mkHushKnob = value:
            if value == null then ''
              # FIGARO_HUSH_APP: inheriting (uses real "figaro" identity)
            ''
            else if value == "@dev" then ''
              : "''${FIGARO_HUSH_APP:=figaro-dev-${name}}"
              export FIGARO_HUSH_APP
              # Isolated, EMBEDDED hush rooted under the dev root: its own
              # identity + encrypted secrets + agent socket (<dir>/run),
              # re-authenticated per shell. Stable per shell (not the
              # nix-shell $TMPDIR), so figaro runs its own hush instance
              # instead of reaching the user's shared agent (whose socket
              # isn't reachable from inside the sandbox).
              : "''${FIGARO_HUSH_DIR:=$FIGARO_DEV_ROOT/hush}"
              export FIGARO_HUSH_DIR
              mkdir -p "''${FIGARO_HUSH_DIR}"
            ''
            else ''
              : "''${FIGARO_HUSH_APP:=${value}}"
              export FIGARO_HUSH_APP
            '';
        in pkgs.mkShell {
          inherit name;
          buildInputs = with pkgs; [
            go gopls gotools
          ] ++ [ figaroPkg ];

          shellHook = ''
            export FIGARO_DEV_ROOT="''${XDG_RUNTIME_DIR:-/tmp}/figaro-dev-${name}"
            mkdir -p "$FIGARO_DEV_ROOT"
            chmod 700 "$FIGARO_DEV_ROOT"

            ${mkKnob "FIGARO_RUNTIME_DIR" "run"    runtime}
            ${mkKnob "FIGARO_CONFIG_DIR"  "config" config}
            ${mkKnob "FIGARO_STATE_DIR"   "state"  state}
            ${mkHushKnob hush}

            # Point figaro/fig/q at THIS worktree's build, in a bin dir
            # prepended to PATH. We symlink all three names (not just q) so a
            # global figaro install can't shadow the dev binary. NOTE: an
            # interactive shell launched inside the dev shell (e.g. fish) may
            # re-prepend its own ~/go/bin or ~/.nix-profile to PATH and shadow
            # this again — guard it by re-prepending $FIGARO_DEV_BIN at the end
            # of your shell rc when it is set (see ~/.config/fish/config.fish).
            export FIGARO_DEV_BIN="$FIGARO_DEV_ROOT/bin"
            mkdir -p "$FIGARO_DEV_BIN"
            for n in figaro fig q; do
              ln -sf "${figaroPkg}/bin/figaro" "$FIGARO_DEV_BIN/$n"
            done
            export PATH="$FIGARO_DEV_BIN:$PATH"

            echo "[figaro-dev:${name}] figaro       = $(command -v figaro)" >&2
            for v in FIGARO_RUNTIME_DIR FIGARO_CONFIG_DIR FIGARO_STATE_DIR FIGARO_HUSH_APP; do
              if [ -n "''${!v:-}" ]; then
                printf "[figaro-dev:${name}] %-20s = %s\n" "$v" "''${!v}" >&2
              else
                printf "[figaro-dev:${name}] %-20s = (inherited)\n" "$v" >&2
              fi
            done
          '';
        };
      in {
        # The default shell — fully inherited environment. Use this
        # to develop against the real daemon/config/state/hush. The
        # in-shell binary is still the worktree build (via
        # buildInputs), so you're testing your changes against your
        # real data. Equivalent to the old default.
        default = mkFigaroShell {
          name = "default";
          runtime = null;
          config  = null;
          state   = null;
          hush    = null;
        };

        # Fully hermetic — every singleton path is dev-scoped.
        # First invocation will trigger the hush + figaro first-run
        # flow. Use this for testing first-run UX or for completely
        # blank-slate experiments.
        clean = mkFigaroShell { name = "clean"; };

        # Share the global hush identity (so you don't have to
        # re-add provider keys) but isolate runtime/config/state.
        # Useful for testing new loadouts/configs against real
        # provider credentials without polluting the real config.
        share-hush = mkFigaroShell {
          name = "share-hush";
          hush = null;
        };

        # Share config (real providers, real loadouts) but isolate
        # runtime + state AND run an isolated, embedded hush (its own
        # identity, re-authenticated per shell — `q login` or set
        # ANTHROPIC_API_KEY on first use). The shared agent's socket
        # isn't reachable from inside the sandbox, so this gives a
        # self-contained hush instead. NOTE: AGE-ENC provider keys in
        # the shared config can't be decrypted by the fresh identity —
        # re-auth the provider (OAuth/login or a plaintext/env key).
        share-config = mkFigaroShell {
          name = "share-config";
          config = null;
          hush   = "@dev";
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
