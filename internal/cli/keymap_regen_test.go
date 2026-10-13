package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// Regenerates the frozen oracles. Run on a green tree only: it records what the
// code does.
//
//	ORACLE_REGEN=1 go test ./internal/cli/ -run TestOracleRegen

func oracleRow(b *strings.Builder, state string, cells map[string]string, bound map[string]string, names []string) {
	inert := ""
	for _, n := range names {
		if _, isBound := bound[n]; !isBound {
			inert = cells[n]
			break
		}
	}
	fmt.Fprintf(b, "\t{%q, %q, map[string]string{\n", state, inert)
	differs := make([]string, 0, len(cells))
	for n, sig := range cells {
		if sig != inert {
			differs = append(differs, n)
		}
	}
	sort.Strings(differs)
	for _, n := range differs {
		fmt.Fprintf(b, "\t\t%q: %q,\n", n, cells[n])
	}
	b.WriteString("\t}},\n")
}

func writeOracle(t *testing.T, path string, b *strings.Builder) {
	t.Helper()
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", path, b.Len())
}

func TestOracleRegen(t *testing.T) {
	if os.Getenv("ORACLE_REGEN") == "" {
		t.Skip("set ORACLE_REGEN=1")
	}
	names := make([]string, 0, 128)
	for i := 0; i < 128; i++ {
		names = append(names, fmt.Sprintf("0x%02x", i))
	}
	var b strings.Builder
	for _, row := range pagerOracle {
		cells, _ := sweepPager(oracleStates[row.state], row.keys)
		oracleRow(&b, row.state, cells, row.keys, names)
	}
	writeOracle(t, "/tmp/pager_oracle.gen", &b)
}

// Green tree only.
func TestOracleRegenInput(t *testing.T) {
	if os.Getenv("ORACLE_REGEN") == "" {
		t.Skip("set ORACLE_REGEN=1")
	}
	names := make([]string, 0)
	for _, k := range inputSweepKeys() {
		names = append(names, k.name)
	}
	var b strings.Builder
	for _, row := range inputOracle {
		cells, _ := sweepInput(t, inputStates[row.state], row.keys)
		oracleRow(&b, row.state, cells, row.keys, names)
	}
	writeOracle(t, "/tmp/input_oracle.gen", &b)
}
