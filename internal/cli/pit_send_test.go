package cli

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

// Gluck, 11-10: '>' on a figaro row shouts an order at it without attending
// it: the attended figaro, the list and its selection stay as they were.
// Every key goes through the input loop's decoder, one byte per read.

type sentTo struct{ to, handle, text string }

type rowSendRig struct {
	in       *interactiveInput
	lt       *livelogTurn
	sent     []sentTo
	attended []string
}

func newRowSendRig(t *testing.T, rows []pitRow) *rowSendRig {
	t.Helper()
	in, lt := navInput(t, &countingWriter{}, true)
	r := &rowSendRig{in: in, lt: lt}
	in.mu.Lock()
	lt.tr.sendTo = func(to, handle, text string) { r.sent = append(r.sent, sentTo{to, handle, text}) }
	lt.tr.attendAria = func(id string) { r.attended = append(r.attended, id) }
	pagerTop(lt.tr)
	lt.tr.setCmdOut("ls", rows)
	in.mu.Unlock()
	return r
}

func (r *rowSendRig) typeSlowly(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if rest, stop := r.in.consume([]byte{s[i]}); stop || len(rest) != 0 {
			t.Fatalf("byte %q: stop=%v held=%q", s[i], stop, rest)
		}
	}
}

// A '>' that missed would leave the rest of the line to the transcript's keys,
// where 'o' opens an editor.
func (r *rowSendRig) openBox(t *testing.T) {
	t.Helper()
	r.typeSlowly(t, ">")
	r.in.mu.Lock()
	open := r.lt.tr.openPit()
	r.in.mu.Unlock()
	if open != pitSendTo {
		t.Fatalf("'>' on a figaro row opened %q, want the send box", open)
	}
}

type pagerPlace struct {
	pit      pitID
	selected string
	top      int
	offset   int
	follow   bool
	subject  string
	mode     keyMode
}

func (r *rowSendRig) place() pagerPlace {
	r.in.mu.Lock()
	defer r.in.mu.Unlock()
	tr := r.lt.tr
	row, _ := tr.pit.selected()
	return pagerPlace{
		pit: tr.pit.id, selected: row.id, top: tr.pit.list().top,
		offset: tr.offset, follow: tr.follow, subject: tr.status.figaroID, mode: tr.mode(),
	}
}

const (
	sendAlpha = "aaaaaaaa11111111aaaaaaaa11111111"
	sendBravo = "bbbbbbbb22222222bbbbbbbb22222222"
	sendRole  = "cccccccc33333333cccccccc33333333"
)

func sendRows() []pitRow {
	return []pitRow{
		staticRow("  ARIA          ID"),
		{text: "  alpha-one", yank: "alpha-one", id: sendAlpha, aria: sendAlpha, to: sendAlpha},
		{text: "  bravo-two", yank: "bravo-two", id: sendBravo, aria: sendBravo, to: sendBravo},
		{text: "  boss role", yank: "boss-role", id: sendRole, aria: sendRole, to: "@" + sendRole},
		{text: "  outfit default@live", yank: "plain-outfit", id: "dddddddd44444444dddddddd44444444"},
	}
}

func TestSendToRowDeliversTheLineAndKeepsThePlace(t *testing.T) {
	r := newRowSendRig(t, sendRows())
	r.typeSlowly(t, "j")
	before := r.place()
	if before.selected != sendBravo || before.mode != modeList || before.follow {
		t.Fatalf("fixture: selected %q in mode %s following %v, want bravo in the list off the tail", before.selected, modeName(before.mode), before.follow)
	}

	r.openBox(t)
	r.in.mu.Lock()
	sigil, title := r.lt.tr.boxSigil(), r.lt.tr.composerTitle()
	r.in.mu.Unlock()
	if sigil != "> bravo-two " || len(title) != 1 || !strings.Contains(stripANSI(title[0]), "to bravo-two") {
		t.Fatalf("the box does not name its target: prompt %q title %q", sigil, title)
	}

	order := "hold the line; quit sHelling, sticky q:l"
	r.typeSlowly(t, order+"\r")

	if len(r.sent) != 1 || r.sent[0] != (sentTo{sendBravo, "bravo-two", order}) {
		t.Fatalf("sent %+v, want exactly %q to bravo", r.sent, order)
	}
	if len(r.attended) != 0 {
		t.Fatalf("sending attended %q", r.attended)
	}
	if after := r.place(); after != before {
		t.Fatalf("the pager moved:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestSendToRowEscapeSendsNothing(t *testing.T) {
	r := newRowSendRig(t, sendRows())
	before := r.place()
	r.openBox(t)
	r.typeSlowly(t, "belay that")
	r.typeSlowly(t, "\x1b")
	if len(r.sent) != 0 {
		t.Fatalf("Esc sent %+v", r.sent)
	}
	if after := r.place(); after != before {
		t.Fatalf("Esc did not return to the list as it was:\nbefore %+v\nafter  %+v", before, after)
	}
	r.typeSlowly(t, ">")
	r.in.mu.Lock()
	open, left := r.lt.tr.openPit(), r.lt.tr.askline.String()
	r.in.mu.Unlock()
	if open != pitSendTo || left != "" {
		t.Fatalf("after Esc, '>' opened %q holding %q; want a fresh send box", open, left)
	}
	r.typeSlowly(t, "\r")
	if len(r.sent) != 0 || len(r.attended) != 0 {
		t.Fatalf("an empty line was sent %+v or attended %q", r.sent, r.attended)
	}
}

func TestSendToRoleRowAddressesTheRole(t *testing.T) {
	r := newRowSendRig(t, sendRows())
	r.typeSlowly(t, "jj")
	r.openBox(t)
	r.typeSlowly(t, "go\r")
	if len(r.sent) != 1 || r.sent[0] != (sentTo{"@" + sendRole, "@boss-role", "go"}) {
		t.Fatalf("sent %+v, want go to @%s", r.sent, sendRole)
	}
}

func TestSendToRowRefusesARowThatIsNotAFigaro(t *testing.T) {
	r := newRowSendRig(t, sendRows())
	r.typeSlowly(t, "jjj>")
	r.in.mu.Lock()
	open := r.lt.tr.openPit()
	r.in.mu.Unlock()
	if open == pitSendTo {
		t.Fatal("the send box opened on an outfit row")
	}
	if len(r.sent) != 0 {
		t.Fatalf("sent %+v", r.sent)
	}
}

func TestSendAddressByKind(t *testing.T) {
	for _, tc := range []struct {
		f    rpc.FigaroInfoResponse
		want string
	}{
		{rpc.FigaroInfoResponse{ID: sendAlpha}, sendAlpha},
		{rpc.FigaroInfoResponse{ID: sendAlpha, Kind: "conversation"}, sendAlpha},
		{rpc.FigaroInfoResponse{ID: sendRole, Kind: "form", TargetAria: sendAlpha}, "@" + sendRole},
		{rpc.FigaroInfoResponse{ID: sendRole, Kind: "form"}, ""},
		{rpc.FigaroInfoResponse{ID: sendRole, Kind: "outfit"}, ""},
		{rpc.FigaroInfoResponse{ID: sendRole, Kind: "null"}, ""},
	} {
		if got := sendAddress(tc.f); got != tc.want {
			t.Errorf("sendAddress(%+v) = %q, want %q", tc.f, got, tc.want)
		}
	}
}

func TestSKeepsStickyInAList(t *testing.T) {
	r := newRowSendRig(t, sendRows())
	sticky := r.lt.view.settings.sticky
	r.typeSlowly(t, "s")
	if r.lt.view.settings.sticky == sticky {
		t.Fatal("s no longer toggles sticky in a list")
	}
	if len(r.sent) != 0 || r.place().pit != pitOutput {
		t.Fatal("s did something besides sticky")
	}
}

func TestTroupeTreeRowsCarryTheirSendAddress(t *testing.T) {
	figs := []rpc.FigaroInfoResponse{
		{ID: "0000aaaa0000aaaa0000aaaa0000aaaa", Kind: "null"},
		{ID: sendAlpha, Kind: "conversation", Parent: "0000aaaa0000aaaa0000aaaa0000aaaa"},
		{ID: sendRole, Kind: "form", TargetAria: sendAlpha, Parent: "0000aaaa0000aaaa0000aaaa0000aaaa"},
	}
	tree := treeFrom(figs, "", -1, "", -1)
	var buf lockedBuffer
	printListRows(&buf, tree.Rows(), 160, true)
	cap := buf.captured()
	to := map[string]string{}
	for _, r := range cap.rows(splitOutputLines(cap.text)) {
		if r.id != "" {
			to[r.id] = r.to
		}
	}
	if to[sendAlpha] != sendAlpha || to[sendRole] != "@"+sendRole || to["0000aaaa0000aaaa0000aaaa0000aaaa"] != "" {
		t.Fatalf("send addresses by row: %v\n%s", to, cap.text)
	}
}

// 'l' opens the troupe's tree, not :ls: its aria rows live in a mounted
// @arias view and name their aria through the view's row key.
func TestSendToARowOfAMountedAriasView(t *testing.T) {
	const beta = "38f4563ac90d4206364a64c0a4f4c4fa"
	f := newFakeMounts()
	f.forms["@things"] = things(
		"items.a.n", `1`, "items.a.name", `"alpha"`, "items.a.id", `"09e4b786557992e7a84fbff7606463f7"`,
		"items.b.n", `2`, "items.b.name", `"beta"`, "items.b.id", `"`+beta+`"`,
	)
	hint := *thingsHint
	hint.Actions = []rpc.ViewAction{{Key: "Enter", Label: "attend", Local: rpc.ViewAttend}}
	f.hint = &hint
	in, lt, _ := mountedPit(t, f, "here")
	r := &rowSendRig{in: in, lt: lt}
	in.mu.Lock()
	lt.tr.sendTo = func(to, handle, text string) { r.sent = append(r.sent, sentTo{to, handle, text}) }
	lt.tr.attendAria = func(id string) { r.attended = append(r.attended, id) }
	in.mu.Unlock()

	r.typeSlowly(t, "/beta\r")
	in.mu.Lock()
	before, _ := lt.tr.pit.selected()
	in.mu.Unlock()
	if before.id != "here\x1fitems.b" {
		t.Fatalf("fixture: landed on %q", before.id)
	}
	r.openBox(t)
	r.typeSlowly(t, "report\r")
	if len(r.sent) != 1 || r.sent[0] != (sentTo{beta, ariaName(beta), "report"}) {
		t.Fatalf("sent %+v, want report to %s", r.sent, beta)
	}
	in.mu.Lock()
	after, _ := lt.tr.pit.selected()
	in.mu.Unlock()
	if len(r.attended) != 0 || after.id != before.id {
		t.Fatalf("attended %q, selection %q -> %q", r.attended, before.id, after.id)
	}
}
