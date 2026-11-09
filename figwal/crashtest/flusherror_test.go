package crashtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isPoisoned(err error) bool {
	return err != nil && strings.Contains(err.Error(), "flushes failing")
}

func chmodTree(t *testing.T, root string, mode os.FileMode) {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFlushErrorReadOnlyRecovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	st, trunk, err := createStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	salt := "flusherr"
	appended := 0
	appendN := func(n int) {
		for i := 0; i < n; i++ {
			q := uint64(appended + 1)
			if _, err := st.AppendMain(trunk, encodePayload(trunk, chanMain, q, q+1, salt)); err != nil {
				t.Fatalf("append %d: %v", appended+1, err)
			}
			appended++
		}
	}
	// While flushes are failing the lineage poisons after a few ticks —
	// Append then rejects by contract. Count only acked appends.
	appendLossy := func(n int) {
		for i := 0; i < n; i++ {
			q := uint64(appended + 1)
			if _, err := st.AppendMain(trunk, encodePayload(trunk, chanMain, q, q+1, salt)); err != nil {
				if isPoisoned(err) {
					return
				}
				t.Fatalf("append %d: %v", appended+1, err)
			}
			appended++
		}
	}
	appendN(60)
	for _, ch := range []string{chanMain, chanNotes, chanState} {
		chDir := filepath.Join(dir, ch)
		if _, err := os.Stat(chDir); err == nil {
			chmodTree(t, chDir, 0o555)
		}
	}
	restore := func() {
		for _, ch := range []string{chanMain, chanNotes, chanState} {
			chDir := filepath.Join(dir, ch)
			if _, err := os.Stat(chDir); err == nil {
				chmodTree(t, chDir, 0o755)
			}
		}
	}
	defer restore()

	st.Kick()
	time.Sleep(3 * flushInterval)
	appendLossy(60)
	st.Kick()
	time.Sleep(3 * flushInterval)

	restore()
	st.Kick()
	time.Sleep(3 * flushInterval)
	// Poison clears on the next successful flush of the lineage; appends
	// must work again.
	appendN(5)
	st.Kick()
	time.Sleep(3 * flushInterval)
	if err := st.Close(); err != nil {
		t.Fatalf("close after recovery: %v", err)
	}

	st2, err := openStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	recs, err := st2.ReadAll(trunk, chanMain)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, r := range recs {
		if p, ours, valid := decodePayload(r.Payload, salt); ours {
			if !valid {
				t.Fatalf("chanLT %d: bad checksum", r.ChanLT)
			}
			got++
			if p.Q != uint64(got) {
				t.Fatalf("q sequence broken at chanLT %d: got %d want %d", r.ChanLT, p.Q, got)
			}
		}
	}
	if got != appended {
		t.Fatalf("appended %d records, %d survived a flush-error window with clean Close", appended, got)
	}
	fmt.Println("flush-error window recovered, all", got, "records intact")
}
