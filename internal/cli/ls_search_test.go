package cli

import (
	"regexp"
	"strings"
	"testing"
)

// Gluck switches figaros by typing a word of a mantra into :ls. Over 59 live
// figaros with the attended one's form mounted, a word found in exactly one
// mantra and no handle left the cursor on that figaro for 18 of 110 such words: the cursor
// went to the first hit drawn, and fuzzy hits spread over long values (the
// attended figaro's skills, other mantras) were drawn first.
func TestAMantraWordLandsOnItsFigaro(t *testing.T) {
	arias, attended := lsSearchFixture(t)
	rows := ariaRows(arias["arias"].(map[string]any), "arias")
	attendedID := attended["aria_id"].(string)
	_, v := lsRig(snapOfJSON(arias), rows, attendedID, snapOfJSON(attended))
	v.Items(200)
	word := regexp.MustCompile(`[A-Za-z][A-Za-z'-]{4,}`)
	var lowered []string
	var handles []string
	for _, r := range rows {
		lowered = append(lowered, strings.ToLower(r.mantra))
		handles = append(handles, r.path[strings.LastIndexByte(r.path, '.')+1:])
	}
	tried, landed := 0, 0
	var missed []string
	seen := map[string]bool{}
	for _, r := range rows {
		for _, w := range word.FindAllString(r.mantra, -1) {
			q := strings.ToLower(strings.Trim(w, "'-"))
			n := 0
			for _, m := range lowered {
				if strings.Contains(m, q) {
					n++
				}
			}
			for _, h := range handles {
				if strings.Contains(h, q) {
					n++
				}
			}
			if n != 1 || seen[q] {
				continue
			}
			seen[q] = true
			tried++
			v.SetQuery(q)
			v.Items(200)
			got := v.Current()
			want := "here" + mountSep + r.path
			rest, ok := strings.CutPrefix(got, want)
			if ok && (rest == "" || rest[0] == '.' && !strings.Contains(rest, ".forks.") && !strings.Contains(rest, mountSep)) {
				landed++
			} else {
				missed = append(missed, q+" -> "+got)
			}
			v.EndQuery("")
		}
	}
	t.Logf("%d of %d words unique to one mantra land on its figaro", landed, tried)
	if tried < 90 {
		t.Fatalf("the fixture yields %d unique words; it no longer measures much", tried)
	}
	if landed*100 < tried*95 {
		t.Errorf("%d of %d land on their figaro, want 95%%; missed:\n%s", landed, tried, strings.Join(missed, "\n"))
	}
}

func TestTheLsRigMountsTheAttendedForm(t *testing.T) {
	arias, attended := lsSearchFixture(t)
	rows := ariaRows(arias["arias"].(map[string]any), "arias")
	_, v := lsRig(snapOfJSON(arias), rows, attended["aria_id"].(string), snapOfJSON(attended))
	n := 0
	for _, r := range v.Items(200) {
		if strings.Count(r.id, mountSep) == 2 {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no row of the attended figaro's form is drawn")
	}
}

// The cursor went to the first hit drawn: the troupe's own ranking was empty,
// and the ranking inside its mounted @arias was never asked. It goes to the
// best hit in any mount: here the figaro named lyric, below one whose mantra
// only mentions lyrics.
func TestASearchLandsOnTheBestHitInAnyMount(t *testing.T) {
	f := newFakeMounts()
	f.forms["@things"] = things(
		"items.a.n", `2`, "items.a.name", `"notes on the lyrics"`, "items.a.id", `"a1"`,
		"items.lyric.n", `1`, "items.lyric.name", `"gen7"`, "items.lyric.id", `"b2"`,
	)
	in, lt, v := mountedPit(t, f, "here")
	feed(t, in, "/lyric")
	if _, n := v.QueryCount(); n != 2 {
		t.Fatalf("/lyric counts %d hits, want both rows", n)
	}
	in.mu.Lock()
	row, _ := lt.tr.pit.selected()
	in.mu.Unlock()
	if row.id != "here"+mountSep+"items.lyric" {
		t.Fatalf("/lyric put the cursor on %q, want the row named lyric", row.id)
	}
}
