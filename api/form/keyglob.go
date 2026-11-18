package form

import "strings"

func KeyGlobMatch(pattern, key string) bool {
	return globMatch(strings.Split(pattern, "."), strings.Split(key, "."))
}

func globMatch(pat, segs []string) bool {
	for len(pat) > 0 {
		p := pat[0]
		if p == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if globMatch(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 || !segmentMatch(p, segs[0]) {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func segmentMatch(pat, seg string) bool {
	for {
		star := strings.IndexByte(pat, '*')
		if star < 0 {
			return pat == seg
		}
		if !strings.HasPrefix(seg, pat[:star]) {
			return false
		}
		seg, pat = seg[star:], pat[star+1:]
		if pat == "" {
			return true
		}
		for i := 0; i <= len(seg); i++ {
			if segmentMatch(pat, seg[i:]) {
				return true
			}
		}
		return false
	}
}
