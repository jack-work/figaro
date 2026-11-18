package provider

import "strings"

func NeutraliseReservedTags(s string) string {
	at := reservedTagAt(s, 0)
	if at < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	from := 0
	for at >= 0 {
		b.WriteString(s[from:at])
		b.WriteString("&lt;")
		from = at + 1
		at = reservedTagAt(s, from)
	}
	b.WriteString(s[from:])
	return b.String()
}

func reservedTagAt(s string, from int) int {
	for {
		i := strings.IndexByte(s[from:], '<')
		if i < 0 {
			return -1
		}
		at := from + i
		if opensReservedTag(s[at+1:]) {
			return at
		}
		from = at + 1
	}
}

func opensReservedTag(s string) bool {
	s = skipSpace(s)
	if strings.HasPrefix(s, "/") {
		s = skipSpace(s[1:])
	}
	if rest, ok := cutFold(s, "system"); ok {
		for len(rest) > 0 && (isSpace(rest[0]) || rest[0] == '_' || rest[0] == '-') {
			rest = rest[1:]
		}
		rest, ok = cutFold(rest, "reminder")
		return ok && !startsWord(rest)
	}
	rest, ok := cutFold(s, "law")
	if !ok {
		return false
	}
	if !startsWord(rest) {
		return true
	}
	rest, ok = cutFold(rest, "s")
	return ok && !startsWord(rest)
}

func skipSpace(s string) string {
	for len(s) > 0 && isSpace(s[0]) {
		s = s[1:]
	}
	return s
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

func cutFold(s, word string) (string, bool) {
	for i := 0; i < len(word); i++ {
		c := word[i]
		switch {
		case s != "" && (s[0] == c || s[0] == c-'a'+'A'):
			s = s[1:]
		case c == 's' && strings.HasPrefix(s, "\u017f"):
			s = s[len("\u017f"):]
		default:
			return s, false
		}
	}
	return s, true
}

func startsWord(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func Reminder(name, body string) string {
	return "<system-reminder name=\"" + escapeReminderName(name) + "\">\n" + NeutraliseReservedTags(body) + "\n</system-reminder>"
}

func escapeReminderName(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

func JoinReminders(texts []string) string {
	return strings.Join(texts, "")
}

func ToolOutput(s string) string { return NeutraliseReservedTags(s) }
