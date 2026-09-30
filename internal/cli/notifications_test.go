package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// NOTIFICATIONS: what figaro told you, still there after it stops saying it.
// The bug this answers was that every alert retired after ten seconds and then
// existed nowhere; each case below is one way that could come back.

func TestNotificationStore(t *testing.T) {
	t.Run("a post is kept after the bar would have retired it", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertError, "3b7aff0a", "turn failed: overloaded")
		got := s.newestFirst()
		if len(got) != 1 || got[0].text != "turn failed: overloaded" || got[0].level != alertError {
			t.Fatalf("%+v", got)
		}
	})

	t.Run("the same thing said again is one row that counts, and moves to the top", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertWarn, "cli", "retry 1 of 3")
		s.post(alertInfo, "cli", "sent")
		s.post(alertWarn, "cli", "retry 2 of 3")
		got := s.newestFirst()
		if len(got) != 2 || got[0].count != 2 || got[0].text != "retry 2 of 3" {
			t.Fatalf("want the retry coalesced, newest wording, on top: %+v", got)
		}
	})

	t.Run("different sources or levels do not coalesce", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertError, "aaaaaaaa", "turn failed")
		s.post(alertError, "bbbbbbbb", "turn failed")
		s.post(alertWarn, "aaaaaaaa", "turn failed")
		if n := len(s.newestFirst()); n != 3 {
			t.Fatalf("%d rows, want 3", n)
		}
	})

	t.Run("unread counts warnings and errors, never info, and a read resets it", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertInfo, "cli", "sent")
		s.post(alertWarn, "cli", "slow")
		if n, worst := s.unread(); n != 1 || worst != alertWarn {
			t.Fatalf("unread %d %v", n, worst)
		}
		s.post(alertError, "cli", "broke")
		if n, worst := s.unread(); n != 2 || worst != alertError {
			t.Fatalf("unread %d %v", n, worst)
		}
		s.markRead()
		if n, _ := s.unread(); n != 0 {
			t.Fatalf("after reading, %d unread", n)
		}
		// A repeat HAPPENED AGAIN: unread again.
		s.post(alertError, "cli", "broke")
		if n, _ := s.unread(); n != 1 {
			t.Fatalf("a repeat after reading should be unread, got %d", n)
		}
	})

	t.Run("the history is bounded, oldest first to go", func(t *testing.T) {
		s := newNotificationStore()
		for i := range notificationHistory + 20 {
			s.post(alertInfo, fmt.Sprintf("src%d", i), "x")
		}
		got := s.newestFirst()
		if len(got) != notificationHistory || got[len(got)-1].source != "src20" {
			t.Fatalf("len %d, oldest %q", len(got), got[len(got)-1].source)
		}
	})

	t.Run("a multi-line note keeps its first line as the row and all of it as the detail", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertError, "cli", "status failed\n  line two\n  line three")
		n := s.newestFirst()[0]
		if n.text != "status failed" || !strings.Contains(n.detail, "line three") {
			t.Fatalf("%+v", n)
		}
	})

	t.Run("clear empties it and leaves nothing unread", func(t *testing.T) {
		s := newNotificationStore()
		s.post(alertError, "cli", "x")
		s.clear()
		if len(s.newestFirst()) != 0 {
			t.Fatal("not cleared")
		}
		if n, _ := s.unread(); n != 0 {
			t.Fatal("cleared but unread")
		}
	})
}

func TestNotificationsView(t *testing.T) {
	s := newNotificationStore()
	s.post(alertInfo, "cli", "sent")
	s.post(alertWarn, "cli", "slow")
	s.post(alertError, "3b7aff0a", "turn failed\nprovider said: overloaded")
	v := &notificationsView{store: s}

	rows := v.Items(80)
	if len(rows) != 3 || !strings.Contains(rows[0].text, "turn failed") || !strings.Contains(rows[0].text, "✗") {
		t.Fatalf("newest first, with its level: %+v", rows)
	}
	if !strings.Contains(rows[0].text, "3b7aff0a") {
		t.Fatalf("the source is on the row: %q", rows[0].text)
	}
	if n, _ := s.unread(); n != 0 {
		t.Fatal("showing the list is reading it")
	}

	t.Run("f cycles the filter", func(t *testing.T) {
		if !v.Key('f') || len(v.Items(80)) != 2 {
			t.Fatal("warnings and up should be two rows")
		}
		if !v.Key('f') || len(v.Items(80)) != 1 {
			t.Fatal("errors alone should be one row")
		}
		v.Key('f')
		if len(v.Items(80)) != 3 {
			t.Fatal("f wraps back to everything")
		}
	})

	t.Run("Enter expands a row to its whole text and folds it back", func(t *testing.T) {
		id := v.Items(80)[0].id
		v.Activate(id)
		rows := v.Items(80)
		if len(rows) < 4 || !strings.Contains(rows[1].text+rows[2].text, "provider said: overloaded") || rows[1].selectable() {
			t.Fatalf("the detail follows its row, unselectable: %+v", rows)
		}
		v.Activate(id)
		if len(v.Items(80)) != 3 {
			t.Fatal("a second Enter folds it")
		}
	})

	t.Run("a is attend: the row's aria, and nothing for a row that has none", func(t *testing.T) {
		rows := v.Items(80)
		if got := v.AriaOf(rows[0].id); got != "3b7aff0a" {
			t.Fatalf("AriaOf = %q", got)
		}
		if got := v.AriaOf(rows[2].id); got != "" {
			t.Fatalf("a cli row names no aria, got %q", got)
		}
	})

	t.Run("y yanks the whole of it", func(t *testing.T) {
		if y := v.Items(80)[0].yank; !strings.Contains(y, "provider said: overloaded") || !strings.Contains(y, "3b7aff0a") {
			t.Fatalf("yank %q", y)
		}
	})

	t.Run("an empty history says so", func(t *testing.T) {
		e := &notificationsView{store: newNotificationStore()}
		if rows := e.Items(80); len(rows) != 1 || rows[0].selectable() {
			t.Fatalf("%+v", rows)
		}
	})
}

// THE MARK: what an alert leaves behind. It is on the bar while there is
// something unread, in the colour of the worst of it, and gone once read.
func TestTheBarKeepsAMarkForWhatIsUnread(t *testing.T) {
	st := newSessionStatus("aria1234", timeZero)
	st.notes = newNotificationStore()
	st.notes.post(alertWarn, "cli", "slow")
	st.notes.post(alertError, "cli", "broke")
	v := st.viewOf(pitNothing, false, timeZero)
	if v.Unread != 2 || v.UnreadLevel != alertError {
		t.Fatalf("view unread %d level %v", v.Unread, v.UnreadLevel)
	}
	if bar := strings.Join(v.render(80), "\n"); !strings.Contains(bar, "𝄞 2") {
		t.Fatalf("the bar should carry the mark:\n%s", bar)
	}
	st.notes.markRead()
	if bar := strings.Join(st.viewOf(pitNothing, false, timeZero).render(80), "\n"); strings.Contains(bar, "𝄞") {
		t.Fatalf("read, and still marked:\n%s", bar)
	}
}

// EVERY NOTE IS POSTED: the verb results that went to the bar, and the long
// ones that went to the message pit, both end up in the history.
func TestCommandNotesArePosted(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.setCommandNote("yanked 12 chars")
	tr.setCommandNoteAt("attend: zzz is not an aria id", alertError)
	got := tr.notes.newestFirst()
	if len(got) != 2 || got[0].level != alertError || got[1].text != "yanked 12 chars" {
		t.Fatalf("%+v", got)
	}
}

// THE HISTORY SURVIVES A HOP. The status is replaced on every subject switch;
// a store that lived on it would forget everything each time.
func TestNotificationsSurviveASubjectSwitch(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.setCommandNoteAt("broke", alertError)
	tr.retarget(tr.client, "bbbbbbbb", newSessionStatus("bbbbbbbb", timeZero), 0)
	if n, _ := tr.status.notes.unread(); n != 1 {
		t.Fatalf("after a hop the new status sees %d unread, want 1", n)
	}
}

// space n opens it; space then anything else is that key, not swallowed.
func TestSpaceNOpensNotifications(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.setCommandNoteAt("broke", alertError)
	tr.dispatch(keyEvent{b: ' ', mode: modeTranscript})
	tr.dispatch(keyEvent{b: 'n', mode: modeTranscript})
	if !tr.showing(pitNotifications) {
		t.Fatalf("space n should open notifications, open is %q", tr.pit.id)
	}
	if n, _ := tr.notes.unread(); n != 0 {
		t.Fatal("opening it reads it")
	}
	tr.dispatch(keyEvent{b: ' ', mode: modeTranscript})
	tr.dispatch(keyEvent{b: 'n', mode: modeTranscript})
	if tr.showing(pitNotifications) {
		t.Fatal("space n again closes it")
	}
	// n alone is still search-repeat, not notifications.
	tr.dispatch(keyEvent{b: 'n', mode: modeTranscript})
	if tr.showing(pitNotifications) {
		t.Fatal("n alone must not open notifications")
	}
}

// The CLI's own warnings reach the history; its echo of every bar alert does
// not, or everything would be there twice.
func TestSlogWarningsAreNotifications(t *testing.T) {
	s := newNotificationStore()
	h := notifyHandler{inner: slog.NewTextHandler(discardWriter{}, nil), store: s}
	l := slog.New(h)
	l.Warn("freeze dump written", "path", "/tmp/x")
	l.Error("tape close", "err", "boom")
	l.Info("chatter")
	l.Warn("figaro session", "report", "sent")
	got := s.newestFirst()
	if len(got) != 2 || got[0].level != alertError || !strings.Contains(got[1].text, "path=/tmp/x") {
		t.Fatalf("%+v", got)
	}
	_ = context.Background()
}

var timeZero = time.Unix(0, 0)

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// `[cli] notice_ttl` is applied, and to every status a hop mints after it. It
// was documented and never applied: every alert held the bar for ten seconds.
func TestNoticeTTLIsAppliedAndSurvivesAHop(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.setNoticeTTL(2 * time.Second)
	if tr.status.noticeTTL != 2*time.Second {
		t.Fatalf("ttl %v", tr.status.noticeTTL)
	}
	tr.retarget(tr.client, "bbbbbbbb", newSessionStatus("bbbbbbbb", timeZero), 0)
	if tr.status.noticeTTL != 2*time.Second {
		t.Fatalf("after a hop the ttl is %v, the constructor's default came back", tr.status.noticeTTL)
	}
}

// A progress line ("…state" while a verb runs) is the bar's, never the
// history's: the result that follows is the news.
func TestProgressIsNotANotification(t *testing.T) {
	tr := jumpFixture(t, 1, 4)
	tr.setProgressNote("…state")
	if got := tr.notes.newestFirst(); len(got) != 0 {
		t.Fatalf("progress was recorded: %+v", got)
	}
	if tr.status.noticeText() != "…state" {
		t.Fatalf("the bar should still show it, shows %q", tr.status.noticeText())
	}
}
