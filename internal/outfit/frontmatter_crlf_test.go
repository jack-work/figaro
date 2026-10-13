package outfit

import "testing"

// The content version hashes the frontmatter, so CRLF and LF copies must agree.
func TestCRLFFrontmatterMatchesItsLFTwin(t *testing.T) {
	lf := "---\nname: golang\ndescription: Go patterns.\n---\nbody\n"
	crlf := "---\r\nname: golang\r\ndescription: Go patterns.\r\n---\r\nbody\r\n"

	got, ok := extractFrontmatter(lf)
	if !ok {
		t.Fatal("LF frontmatter not recognised")
	}
	crGot, crOK := extractFrontmatter(crlf)
	if !crOK {
		t.Fatal("CRLF frontmatter not recognised: the whole file would land in the form")
	}
	if crGot != got {
		t.Errorf("CRLF yields a different frontmatter string, so the same skill mints a different stump:\n LF   %q\n CRLF %q", got, crGot)
	}
}
