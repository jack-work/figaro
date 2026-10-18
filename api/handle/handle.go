package handle

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed words.txt
var wordsFile string

var (
	words  = strings.Fields(wordsFile)
	byStem = func() map[string]int {
		m := make(map[string]int, len(words))
		for i, w := range words {
			m[w[:StemLen]] = i
		}
		return m
	}()
)

const (
	Separator = "-"
	StemLen   = 3
	BitsPer   = 10
	MinWords  = 2
	MaxWords  = 3
	HexLen    = MinWords * BitsPer / 4
)

func Words(id string, n int) string {
	sigil, body := split(id)
	if n < MinWords || n > MaxWords || len(body) < 8 {
		return ""
	}
	top, err := strconv.ParseUint(body[:8], 16, 32)
	if err != nil {
		return ""
	}
	out := make([]string, n)
	for i := range out {
		out[i] = words[(top>>(32-BitsPer*(i+1)))&(1<<BitsPer-1)]
	}
	return sigil + strings.Join(out, Separator)
}

type Ref struct {
	Sigil string
	Bits  uint32
	N     int
}

func Parse(s string) (Ref, bool) {
	sigil, body := split(strings.ToLower(s))
	parts := strings.Split(body, Separator)
	if len(parts) < MinWords || len(parts) > MaxWords {
		return Ref{}, false
	}
	var bits uint32
	for _, p := range parts {
		i, ok := index(p)
		if !ok {
			return Ref{}, false
		}
		bits = bits<<BitsPer | uint32(i)
	}
	return Ref{Sigil: sigil, Bits: bits << (32 - BitsPer*len(parts)), N: len(parts)}, true
}

func (r Ref) HexPrefix() string {
	full := strconv.FormatUint(uint64(r.Bits), 16)
	full = strings.Repeat("0", 8-len(full)) + full
	return r.Sigil + full[:r.N*BitsPer/4]
}

func (r Ref) Matches(id string) bool {
	w := Words(id, r.N)
	return w != "" && w == r.String()
}

func (r Ref) String() string {
	out := make([]string, r.N)
	for i := range out {
		out[i] = words[(r.Bits>>(32-BitsPer*(i+1)))&(1<<BitsPer-1)]
	}
	return r.Sigil + strings.Join(out, Separator)
}

func index(part string) (int, bool) {
	if len(part) < StemLen {
		return 0, false
	}
	i, ok := byStem[part[:StemLen]]
	if !ok || !strings.HasPrefix(words[i], part) {
		return 0, false
	}
	return i, true
}

func split(s string) (string, string) {
	if strings.HasPrefix(s, "@") {
		return "@", s[1:]
	}
	return "", s
}

func IsPrefix(s string) bool {
	_, body := split(s)
	if len(body) < StemLen || strings.HasPrefix(body, Separator) {
		return false
	}
	for _, r := range body {
		if r != '-' && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}
