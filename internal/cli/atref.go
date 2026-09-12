// Package cli: the form reference sigil.
//
// `@key!` used to be expanded HERE, on the client, permissively: a key that
// was not on the board stayed literal and nobody was told. Expansion now runs
// on the daemon, beside the quote coordinate, under one policy: a terminated
// token that does not resolve refuses the message (internal/figaro/
// input_rewrite.go). What remains client-side is the sigil, which tab
// completion needs to offer keys.
package cli

// refSigil is the prefix character for form references. Set from config at
// startup; default '@'.
var refSigil byte = '@'

// SetRefSigil configures the form reference prefix. Called once at CLI
// startup from the loaded config.
func SetRefSigil(s string) {
	if len(s) == 1 && (s[0] == '@' || s[0] == ':') {
		refSigil = s[0]
	}
}
