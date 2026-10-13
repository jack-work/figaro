package cli

import "testing"

func TestFormatContextUsage(t *testing.T) {
	cases := []struct {
		tokens, limit int
		exact         bool
		want          string
	}{
		// The limit is known before the first turn; zero is never an estimate.
		{0, 1_000_000, true, "0/1.0m 0.0%"},
		{0, 1_000_000, false, "0/1.0m 0.0%"},
		{0, 0, true, "-"},
		{-1, 0, true, "-"},
		{-1, 1_000_000, true, "0/1.0m 0.0%"},
		{135_000, 0, true, "135.0k"},
		{135_000, 0, false, "~135.0k"},
		{135_000, 1_000_000, true, "135.0k/1.0m 13.5%"},
		{135_075, 1_000_000, false, "~135.1k/1.0m 13.5%"},
		{190_000, 200_000, true, "190.0k/200.0k 95.0%"},
		{1_100_000, 1_000_000, true, "1.1m/1.0m 110.0%"},
	}
	for _, tc := range cases {
		if got := formatContextUsage(tc.tokens, tc.limit, tc.exact); got != tc.want {
			t.Errorf("formatContextUsage(%d, %d, %t) = %q, want %q", tc.tokens, tc.limit, tc.exact, got, tc.want)
		}
	}
}

func TestFormatCtxCell(t *testing.T) {
	cases := []struct {
		tokens int
		want   string
	}{
		{820, "820"},
		{2_075, "2k"},
		{135_075, "135k"},
		{999_999, "999k"},
		{1_000_000, "1.0m"},
		{1_350_000, "1.4m"},
	}
	for _, tc := range cases {
		if got := formatCtxCell(tc.tokens); got != tc.want {
			t.Errorf("formatCtxCell(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}
