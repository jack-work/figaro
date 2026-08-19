package xwal

import (
	"runtime"
	"strings"
)

// A NODE KEY IS NOT A FILENAME.
//
// Node keys are chosen by the caller and used verbatim as path components, so
// a key that is legal in the id namespace can be illegal on a filesystem.
// Windows reserves < > : " | ? * in a path component (and \ / everywhere), and
// figaro names a derived form "@libretto::<id>" -- which mkdir refuses on
// Windows and accepts on Linux, so the same store is portable in one direction
// only.
//
// Percent-encoding, because it has the one property that matters here: it is
// the IDENTITY for every key that contains no reserved character. Existing
// stores are byte-for-byte unchanged, and nothing needs migrating.
const fsReserved = `<>:"|?*%`

// fsEncode gates the whole mechanism to the platform that needs it. The
// reserved set is WINDOWS's; on POSIX every one of those bytes is a legal
// path component, existing stores already hold literal names (figaro's
// "@libretto::<id>" among them), and encoding on read made every such
// node unreachable -- the daemon could not start on a Linux store
// carrying a libretto. So a POSIX store is byte-for-byte identity in
// both directions, and only Windows -- where no literal reserved name
// can exist, because mkdir refuses it -- represents keys encoded.
var fsEncode = runtime.GOOS == "windows"

func fsName(key string) string {
	if !fsEncode || !strings.ContainsAny(key, fsReserved) {
		return key // the overwhelmingly common case, and it allocates nothing
	}
	var b strings.Builder
	b.Grow(len(key) + 8)
	for i := 0; i < len(key); i++ {
		if c := key[i]; strings.IndexByte(fsReserved, c) >= 0 {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		} else {
			b.WriteByte(key[i])
		}
	}
	return b.String()
}

// fsNames is fsName over a lineage: a branch is a chain of node keys, and it
// is composed into a path element by element.
func fsNames(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fsName(k)
	}
	return out
}

// keyName is fsName's inverse: the key a directory name stands for.
func keyName(name string) string {
	if !fsEncode || !strings.Contains(name, "%") {
		return name
	}
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		if name[i] == '%' && i+2 < len(name) {
			if hi, lo := unhex(name[i+1]), unhex(name[i+2]); hi >= 0 && lo >= 0 {
				b.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		b.WriteByte(name[i])
	}
	return b.String()
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
