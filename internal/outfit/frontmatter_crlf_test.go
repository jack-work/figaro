package outfit

import "testing"

// A skill saved with Windows line endings must produce the SAME frontmatter
// as its LF twin. Not cosmetic: the frontmatter lands in the form
// verbatim and the outfit's content version is a hash of that patch, so a
// stray \r mints a second outfit stump: two shared prefixes and two caches
// for one logical outfit, across two machines editing one repository.
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
