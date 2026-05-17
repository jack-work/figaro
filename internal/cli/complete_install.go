package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jack-work/figaro/internal/cmdkit"
)

// runCompletionInstall writes the generated completion script for
// shellArg (or the detected shell, when empty) to the standard
// autoload path for that shell. Creates intermediate directories.
func runCompletionInstall(r *cmdkit.Router, shellArg string) error {
	shell, err := resolveShell(shellArg)
	if err != nil {
		return err
	}
	path, note, err := completionInstallPath(shell)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := r.WriteCompletion(f, shell); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s completion to %s\n", shell, path)
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return nil
}

// resolveShell returns the requested shell, or detects it from
// $SHELL when arg is empty.
func resolveShell(arg string) (cmdkit.CompletionShell, error) {
	if arg != "" {
		switch arg {
		case "bash", "zsh", "fish":
			return cmdkit.CompletionShell(arg), nil
		default:
			return "", fmt.Errorf("unsupported shell %q (use bash, zsh, or fish)", arg)
		}
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		return "", fmt.Errorf("$SHELL is unset; pass the shell name explicitly")
	}
	switch base := filepath.Base(sh); base {
	case "bash", "zsh", "fish":
		return cmdkit.CompletionShell(base), nil
	default:
		return "", fmt.Errorf("could not detect shell from $SHELL=%q; pass it explicitly", sh)
	}
}

// completionInstallPath resolves the standard autoload path for a
// shell. The optional note is printed to stderr after install (for
// extra setup steps the user may need to take).
func completionInstallPath(shell cmdkit.CompletionShell) (path, note string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("user home: %w", err)
	}
	switch shell {
	case cmdkit.ShellFish:
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "fish", "completions", "figaro.fish"), "", nil

	case cmdkit.ShellBash:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "bash-completion", "completions", "figaro"),
			"note: requires the bash-completion package to autoload on first tab", nil

	case cmdkit.ShellZsh:
		dir := filepath.Join(home, ".zsh", "completions")
		return filepath.Join(dir, "_figaro"),
			fmt.Sprintf("note: ensure %s is on $fpath before `compinit` in your .zshrc:\n  fpath=(%s $fpath)\n  autoload -Uz compinit && compinit", dir, dir), nil

	default:
		return "", "", fmt.Errorf("unsupported shell: %s", shell)
	}
}
