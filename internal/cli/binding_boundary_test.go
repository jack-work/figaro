package cli

import (
	"reflect"
	"testing"
)

// A prompt is not argv: the pre-verb binding flags stop at the first bare
// `--`. Before this, `figaro send -- explain --no-bind` deleted the token
// from the PROMPT and flipped the binding policy on the way past.
func TestExtractNoBindStopsAtTheBoundary(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		want     []string
		wantFlag bool
	}{
		{
			name: "consumed before the boundary",
			in:   []string{"send", "--no-bind", "--", "hi"},
			want: []string{"send", "--", "hi"}, wantFlag: true,
		},
		{
			name: "left alone after the boundary",
			in:   []string{"send", "--", "explain", "--no-bind", "and", "-A", "please"},
			want: []string{"send", "--", "explain", "--no-bind", "and", "-A", "please"},
		},
		{
			name: "bare form still consumes it",
			in:   []string{"--absolute", "--", "hi"},
			want: []string{"--", "hi"}, wantFlag: true,
		},
		{
			name: "no boundary at all is unchanged behaviour",
			in:   []string{"list", "-A"},
			want: []string{"list"}, wantFlag: true,
		},
		{
			name: "only the FIRST boundary counts",
			in:   []string{"send", "--", "a", "--", "-A"},
			want: []string{"send", "--", "a", "--", "-A"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noBindFlag = false
			in := append([]string(nil), tc.in...)
			got := extractNoBindFlag(in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argv: got %q, want %q", got, tc.want)
			}
			if noBindFlag != tc.wantFlag {
				t.Errorf("noBindFlag: got %v, want %v", noBindFlag, tc.wantFlag)
			}
			noBindFlag = false
		})
	}
}
