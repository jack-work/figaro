package cli

// THE TEST BINARY DOES NOT TOUCH THE REAL CLIPBOARD. Every test in this package
// runs with paste stubbed to "nothing on the clipboard", because a test that
// shells out to wl-paste asserts against whatever the person running it last
// copied -- which is not a test, it is a coin flip that happened to be green
// on the machine where it was written. A test that wants paste sets this
// itself, for the duration it needs.
func init() {
	clipboardRead = func() (string, error) { return "", nil }
}
