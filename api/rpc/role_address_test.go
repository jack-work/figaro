package rpc

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func splitRoleAddresses(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for s = strings.TrimLeft(s, " "); s != ""; s = strings.TrimLeft(s, " ") {
		if s[0] == '"' {
			q, err := strconv.QuotedPrefix(s)
			if err != nil {
				t.Fatalf("%q: %v", s, err)
			}
			a, _ := strconv.Unquote(q)
			out, s = append(out, a), s[len(q):]
			continue
		}
		a, rest, _ := strings.Cut(s, " ")
		out, s = append(out, a), rest
	}
	return out
}

// Role names may hold spaces ("@El Zapador"); a joined list of addresses
// must still split back into the roles it was made from.
func TestJoinedRoleAddressesSplitBack(t *testing.T) {
	for _, roles := range [][]string{
		{"@edge"},
		{"@El Zapador", "@edge"},
		{"@Coordinador de Spain", "@King of Spain", "@paperd-figaro-identity-plan"},
		{`@say "hi"`, `@back\slash`, "@tab\there", "@abcde"},
	} {
		joined := JoinRoleAddresses(roles)
		if got := splitRoleAddresses(t, joined); !slices.Equal(got, roles) {
			t.Fatalf("%q joined as %s splits back to %q", roles, joined, got)
		}
	}
	if got := JoinRoleAddresses([]string{"@El Zapador", "@edge"}); got != `"@El Zapador" @edge` {
		t.Fatalf("joined as %s", got)
	}
}
