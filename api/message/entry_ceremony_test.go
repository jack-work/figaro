package message

import "testing"

func TestAnEmptyLogEntryIsNotCeremonial(t *testing.T) {
	blank := SenderText("clerk", "")
	blank.Principal = "user:1000"
	m := Message{Role: RoleInput, Content: []Content{blank}}
	if !IsEntry(m) || IsCeremonial(m) {
		t.Fatalf("an empty entry: IsEntry=%v IsCeremonial=%v; turn_count, ls MSGS and gc --empty must agree", IsEntry(m), IsCeremonial(m))
	}
	if CountMessages([]Message{m}) != 1 {
		t.Fatal("an empty entry is not counted as a message")
	}
	if !IsCeremonial(Message{Role: RoleInput}) {
		t.Fatal("an input with no content is no longer ceremonial")
	}
}
