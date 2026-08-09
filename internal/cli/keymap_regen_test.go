package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// Regenerates the two frozen oracles against the CURRENT signature format.
//
// Run on a GREEN tree only: it records what the code does, so regenerating
// over a change installs that change as the definition of correct. The safe
// sequence is regenerate, confirm the suite still passes, THEN make the change.
//
//	ORACLE_REGEN=1 go test ./internal/cli/ -run TestOracleRegen
func TestOracleRegen(t *testing.T) {
	if os.Getenv("ORACLE_REGEN") == "" {
		t.Skip("set ORACLE_REGEN=1")
	}
	var b strings.Builder
	for _, row := range pagerOracle {
		setup := oracleStates[row.state]
		base := inertOffset(setup, row.keys)
		inert := ""
		cells := map[string]string{}
		record := func(name string, press func(*transcript)) {
			tr := oracleTranscript()
			setup(tr)
			press(tr)
			cells[name] = oracleSignature(tr, base)
		}
		for i := 0; i < 128; i++ {
			c := byte(i)
			record(fmt.Sprintf("0x%02x", c), func(tr *transcript) { tr.key(c) })
		}
		for n := navUp; n <= navEnd; n++ {
			n := n
			record(navName(n), func(tr *transcript) { tr.navMotion(n) })
		}
		// The inert signature is whatever an unbound byte leaves.
		for i := 0; i < 128; i++ {
			n := fmt.Sprintf("0x%02x", i)
			if _, bound := row.keys[n]; !bound {
				inert = cells[n]
				break
			}
		}
		fmt.Fprintf(&b, "\t{%q, %q, map[string]string{\n", row.state, inert)
		names := make([]string, 0, len(cells))
		for n, sig := range cells {
			if sig != inert {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "\t\t%q: %q,\n", n, cells[n])
		}
		b.WriteString("\t}},\n")
	}
	if err := os.WriteFile("/tmp/pager_oracle.gen", []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote /tmp/pager_oracle.gen (%d bytes)", b.Len())
}

// The input loop's half. Same rules: green tree only.
func TestOracleRegenInput(t *testing.T) {
	if os.Getenv("ORACLE_REGEN") == "" {
		t.Skip("set ORACLE_REGEN=1")
	}
	var b strings.Builder
	for _, row := range inputOracle {
		build := inputStates[row.state]
		base := inputInertOffset(t, build, row.keys)
		cells := map[string]string{}
		for _, k := range inputSweepKeys() {
			p := build(t)
			rest, stop := p.in.consume([]byte(k.data))
			settleProbe(t, p)
			cells[k.name] = inputSignature(p, stop, rest, base)
		}
		inert := ""
		for _, k := range inputSweepKeys() {
			if _, bound := row.keys[k.name]; !bound {
				inert = cells[k.name]
				break
			}
		}
		fmt.Fprintf(&b, "\t{%q, %q, map[string]string{\n", row.state, inert)
		names := make([]string, 0, len(cells))
		for n, sig := range cells {
			if sig != inert {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "\t\t%q: %q,\n", n, cells[n])
		}
		b.WriteString("\t}},\n")
	}
	if err := os.WriteFile("/tmp/input_oracle.gen", []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote /tmp/input_oracle.gen (%d bytes)", b.Len())
}
