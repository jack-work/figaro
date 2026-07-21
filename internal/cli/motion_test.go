package cli

import "testing"

type motionCase struct {
	name string
	v    string
	col  int
	want int
}

func runMotionCases(t *testing.T, fn func(string, int) int, cases []motionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fn(tc.v, tc.col); got != tc.want {
				t.Errorf("%s col=%d: got %d, want %d", tc.v, tc.col, got, tc.want)
			}
		})
	}
}

func TestMotionWordForward(t *testing.T) {
	runMotionCases(t, wordForward, []motionCase{
		{"simple", "foo bar", 0, 4},
		{"mid word", "foo bar", 1, 4},
		{"last word to eol", "foo bar", 4, 6},
		{"word to punct", "foo.bar", 0, 3},
		{"punct to word", "foo.bar", 3, 4},
		{"underscore is word", "foo_bar baz", 0, 8},
		{"from leading space", "  foo", 0, 2},
		{"trailing space stops on last rune", "foo   ", 0, 5},
		{"empty", "", 0, 0},
		{"col past end", "foo", 10, 2},
		{"wide rune word", "宽度 test", 0, 7},
		{"col mid-rune clamps", "宽度 test", 1, 7},
		{"single word char to punct run", "a==b", 0, 1},
		{"punct run to word", "a==b", 1, 3},
		{"word to punct mid line", "one two.three", 4, 7},
	})
}

func TestMotionWordEnd(t *testing.T) {
	runMotionCases(t, wordEnd, []motionCase{
		{"to current word end", "foo bar", 0, 2},
		{"at end goes to next", "foo bar", 2, 6},
		{"word before punct", "foo.bar", 0, 2},
		{"word end to punct", "foo.bar", 2, 3},
		{"punct to word end", "foo.bar", 3, 6},
		{"from leading space", "  foo", 0, 4},
		{"wide rune run end", "宽度x", 0, 6},
		{"stuck at eol", "ab", 1, 1},
		{"trailing space sticks at last rune", "foo  ", 2, 4},
		{"col mid-rune clamps", "宽度", 1, 3},
		{"empty", "", 5, 0},
	})
}

func TestMotionWordBack(t *testing.T) {
	runMotionCases(t, wordBack, []motionCase{
		{"word start to prev word", "foo bar", 4, 0},
		{"from eol", "foo bar", 6, 4},
		{"mid word to its start", "foo bar", 5, 4},
		{"word to punct", "foo.bar", 4, 3},
		{"punct to prev word", "foo.bar", 3, 0},
		{"across run of spaces", "foo  bar", 5, 0},
		{"at col zero", "foo bar", 0, 0},
		{"ascii back over wide runes", "宽度 test", 7, 0},
		{"mid ascii after wide", "宽度 test", 9, 7},
		{"leading blanks to zero", "  foo", 2, 0},
		{"col past end clamps", "abc", 100, 0},
		{"col mid-rune clamps", "宽度", 4, 0},
		{"empty", "", 3, 0},
	})
}

func TestMotionFirstNonBlank(t *testing.T) {
	cases := []struct {
		name string
		v    string
		want int
	}{
		{"leading spaces", "  foo", 2},
		{"no indent", "foo", 0},
		{"all blank", "   ", 0},
		{"empty", "", 0},
		{"tab indent", "\t x", 2},
		{"wide rune", "  宽", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonBlank(tc.v); got != tc.want {
				t.Errorf("%q: got %d, want %d", tc.v, got, tc.want)
			}
		})
	}
}

func TestMotionWalkLine(t *testing.T) {
	v := "one two.three  four"
	wantW := []int{4, 7, 8, 15, 18, 18}
	col := 0
	for i, want := range wantW {
		col = wordForward(v, col)
		if col != want {
			t.Fatalf("w step %d: got %d, want %d", i, col, want)
		}
	}
	wantB := []int{15, 8, 7, 4, 0, 0}
	for i, want := range wantB {
		col = wordBack(v, col)
		if col != want {
			t.Fatalf("b step %d: got %d, want %d", i, col, want)
		}
	}
}
