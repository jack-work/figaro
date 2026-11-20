package chat

import "testing"

func TestACursorSurvivesBeingWrittenAsText(t *testing.T) {
	for _, c := range []Cursor{{}, {Turn: 7, Text: 12, Started: 2, Ended: 1}, {Turn: 3, Begun: true}} {
		got, err := ParseCursor(c.String())
		if err != nil || got != c {
			t.Fatalf("%+v -> %q -> %+v, %v", c, c.String(), got, err)
		}
	}
}

func TestAFourNumberCursorParsesWithoutTheBegunFlag(t *testing.T) {
	got, err := ParseCursor("4.5.6.7")
	if err != nil || got != (Cursor{Turn: 4, Text: 5, Started: 6, Ended: 7}) {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestMalformedCursorsAreRefused(t *testing.T) {
	for _, s := range []string{"", "1", "1.2.3", "1.2.3.x", "1.2.3.-4", "1.2.3.4.2", "1.2.3.4.1.0"} {
		if _, err := ParseCursor(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
}
