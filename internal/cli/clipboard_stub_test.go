package cli

// Every test runs with paste stubbed: a shell-out would read the developer's clipboard.
func init() {
	clipboardRead = func() (string, error) { return "", nil }
}
