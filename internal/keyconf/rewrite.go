package keyconf

import (
	"fmt"
	"strconv"
	"strings"
)

type table struct {
	path   []string
	header string
}

func bindTable(layer string) table {
	return table{path: []string{"bind", layer}, header: fmt.Sprintf("[bind.%s]", quoteKey(layer))}
}

func layerBindTable(name string) table {
	return table{path: []string{"layer", name, "bind"}, header: fmt.Sprintf("[layer.%s.bind]", quoteKey(name))}
}

func (t table) is(path []string) bool {
	if len(path) != len(t.path) {
		return false
	}
	for i := range path {
		if path[i] != t.path[i] {
			return false
		}
	}
	return true
}

type srcLine struct {
	text  string
	table []string
	key   string
	isKey bool
}

func parseLines(src string) []srcLine {
	raw := strings.SplitAfter(src, "\n")
	if n := len(raw); n > 0 && raw[n-1] == "" {
		raw = raw[:n-1]
	}
	var cur []string
	out := make([]srcLine, 0, len(raw))
	for _, text := range raw {
		l := srcLine{text: text, table: cur}
		trim := strings.TrimSpace(text)
		switch {
		case strings.HasPrefix(trim, "[[") || trim == "" || strings.HasPrefix(trim, "#"):
		case strings.HasPrefix(trim, "["):
			if path, ok := headerPath(trim); ok {
				cur = path
			} else {
				cur = nil
			}
			l.table = cur
		default:
			if key, ok := lineKey(trim); ok {
				l.key, l.isKey = key, true
			}
		}
		out = append(out, l)
	}
	return out
}

func headerPath(trim string) ([]string, bool) {
	end := strings.LastIndexByte(trim, ']')
	if end < 0 {
		return nil, false
	}
	inner := strings.TrimSpace(trim[1:end])
	var path []string
	for inner != "" {
		seg, rest, ok := keyPart(inner)
		if !ok {
			return nil, false
		}
		path = append(path, seg)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if rest[0] != '.' {
			return nil, false
		}
		inner = strings.TrimSpace(rest[1:])
	}
	return path, len(path) > 0
}

func lineKey(trim string) (string, bool) {
	key, rest, ok := keyPart(trim)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	return key, true
}

func keyPart(s string) (key, rest string, ok bool) {
	switch {
	case strings.HasPrefix(s, `"`):
		end := closingQuote(s)
		if end < 0 {
			return "", "", false
		}
		key, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return "", "", false
		}
		return key, s[end+1:], true
	case strings.HasPrefix(s, "'"):
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", "", false
		}
		return s[1 : end+1], s[end+2:], true
	}
	i := 0
	for i < len(s) && (s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9' || s[i] == '-' || s[i] == '_') {
		i++
	}
	if i == 0 {
		return "", "", false
	}
	return s[:i], s[i:], true
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

func rebound(text, action string) string {
	eq := strings.IndexByte(text, '=')
	value := strings.TrimLeft(text[eq+1:], " \t")
	tail := ""
	switch {
	case strings.HasPrefix(value, `"`):
		if end := closingQuote(value); end >= 0 {
			tail = value[end+1:]
		}
	case strings.HasPrefix(value, "'"):
		if end := strings.IndexByte(value[1:], '\''); end >= 0 {
			tail = value[end+2:]
		}
	}
	if !strings.HasSuffix(tail, "\n") && strings.HasSuffix(text, "\n") {
		tail += "\n"
	}
	return strings.TrimRight(text[:eq], " \t") + " = " + strconv.Quote(action) + tail
}

func bindLine(keys, action string) string {
	return fmt.Sprintf("%s = %q\n", quoteKey(keys), action)
}

func rewrite(src string, before, after File) string {
	lines := parseLines(src)
	edit := func(t table, was, now map[string]string) {
		for _, k := range sortedKeys(was) {
			if _, kept := now[k]; kept {
				continue
			}
			for i := range lines {
				if lines[i].isKey && lines[i].key == k && t.is(lines[i].table) {
					lines = append(lines[:i], lines[i+1:]...)
					break
				}
			}
		}
		for _, k := range sortedKeys(now) {
			action := now[k]
			if old, had := was[k]; had {
				if old == action {
					continue
				}
				for i := range lines {
					if lines[i].isKey && lines[i].key == k && t.is(lines[i].table) {
						lines[i].text = rebound(lines[i].text, action)
						break
					}
				}
				continue
			}
			at := -1
			for i := range lines {
				if !t.is(lines[i].table) {
					continue
				}
				if lines[i].isKey || at < 0 {
					at = i
				}
			}
			add := srcLine{text: bindLine(k, action), table: t.path, key: k, isKey: true}
			if at < 0 {
				if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1].text, "\n") {
					lines[n-1].text += "\n"
				}
				if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1].text) != "" {
					lines = append(lines, srcLine{text: "\n"})
				}
				lines = append(lines, srcLine{text: t.header + "\n", table: t.path}, add)
				continue
			}
			lines = append(lines[:at+1], append([]srcLine{add}, lines[at+1:]...)...)
		}
	}
	for _, layer := range unionKeys(before.Bind, after.Bind) {
		edit(bindTable(layer), before.Bind[layer], after.Bind[layer])
	}
	for _, name := range unionKeys(before.Layer, after.Layer) {
		edit(layerBindTable(name), before.Layer[name].Bind, after.Layer[name].Bind)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
	}
	return b.String()
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return sortedKeys(seen)
}
