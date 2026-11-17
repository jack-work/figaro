package perffix

import (
	"os"
	"path/filepath"
	"testing"
)

const Env = "FIGARO_PERF_FIXTURES"

func Dir(tb testing.TB, name string, build func(dir string)) string {
	tb.Helper()
	root := os.Getenv(Env)
	if root == "" {
		dir := tb.TempDir()
		build(dir)
		return dir
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(filepath.Join(dir, ".built")); err == nil {
		return dir
	}
	if err := os.RemoveAll(dir); err != nil {
		tb.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tb.Fatal(err)
	}
	build(dir)
	if err := os.WriteFile(filepath.Join(dir, ".built"), nil, 0o600); err != nil {
		tb.Fatal(err)
	}
	return dir
}
