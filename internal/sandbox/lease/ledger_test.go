package lease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheLedgerSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases", "ledger.json")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Found() {
		t.Fatal("a new ledger says it was found")
	}
	if err := l.Begin("spain", "shell", 3, "f1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Leased("shell", 3); err == nil {
		t.Fatal("a lease went live with no sandbox picked")
	}
	if _, err := l.Picked("shell", 3, "shell0", "unix:///x"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Leased("shell", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddHolder("shell", 3, "f2"); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := again.Of("shell0")
	if !again.Found() || !ok || row.State != Leased || !row.Holds("f1") || !row.Holds("f2") || row.Exec != "unix:///x" {
		t.Fatalf("after reopen: %+v %v", row, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger mode %v, %v", info.Mode(), err)
	}
	if again.Floor("shell") != 3 {
		t.Fatalf("floor %d", again.Floor("shell"))
	}
}

func TestAHolderDropsAndTheReleaseForgetsEveryHolder(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	l.Begin("spain", "shell", 1, "f1")
	l.Picked("shell", 1, "shell0", "unix:///x")
	l.Leased("shell", 1)
	l.AddHolder("shell", 1, "f2")
	row, err := l.DropHolder("shell", 1, "f1")
	if err != nil || len(row.Holders) != 1 || !row.Holds("f2") {
		t.Fatalf("%+v %v", row, err)
	}
	row, err = l.Releasing("shell", 1, "n")
	if err != nil || len(row.Holders) != 0 || row.Nonce != "n" || row.State != Releasing {
		t.Fatalf("%+v %v", row, err)
	}
	if _, err := l.AddHolder("shell", 1, "f3"); err == nil {
		t.Fatal("a releasing lease took a holder")
	}
	if err := l.Close("shell", 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Of("shell0"); ok {
		t.Fatal("a closed row still names its sandbox")
	}
}

func TestALedgerThisBuildDidNotWriteDoesNotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	os.WriteFile(path, []byte(`{"version":1,"rows":[],"extra":true}`), 0o600)
	if _, err := Open(path); err == nil {
		t.Fatal("an unknown field was ignored")
	}
}
