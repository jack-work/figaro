package cli

import "testing"

func TestABlankStdinIsRefusedBeforeMintingAFigaroButNotALog(t *testing.T) {
	plain, err := parseDress("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	plain.exact = true
	if !blankStdin(plain, " \n") || plain.mintsLog() {
		t.Fatal("a blank stdin minting a figaro is not refused")
	}
	logged, err := parseDress("", "system.kind=log", "")
	if err != nil {
		t.Fatal(err)
	}
	logged.exact = true
	if !logged.mintsLog() {
		t.Fatal("system.kind=log does not mint a log")
	}
	if blankStdin(plain, "q") {
		t.Fatal("a prompt counted as blank")
	}
}
