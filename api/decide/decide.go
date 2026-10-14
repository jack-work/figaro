package decide

import (
	"fmt"
	"strings"
)

const Sigil = "??"

const (
	openBrace  = '{'
	closeBrace = '}'
	escape     = '\\'
)

func Parse(prompt string) (string, []string, bool) {
	tail, ok := sigilLine(prompt)
	if !ok {
		if tail.escaped {
			return tail.unescaped, nil, false
		}
		return prompt, nil, false
	}
	options := tail.options()
	if len(options) == 0 {
		return prompt, nil, false
	}
	return tail.body, options, true
}

func Bare(prompt string) (string, bool) {
	tail, ok := sigilLine(prompt)
	if !ok || len(tail.options()) > 0 {
		return "", false
	}
	return tail.body, true
}

type tailLine struct {
	body      string
	rest      string
	escaped   bool
	unescaped string
}

func sigilLine(prompt string) (tailLine, bool) {
	lines := strings.Split(prompt, "\n")
	last := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = i
			break
		}
	}
	if last < 0 {
		return tailLine{}, false
	}
	line := strings.TrimLeft(lines[last], " \t")
	if strings.HasPrefix(line, string(escape)+Sigil) {
		lines[last] = strings.Replace(lines[last], string(escape)+Sigil, Sigil, 1)
		return tailLine{escaped: true, unescaped: strings.Join(lines, "\n")}, false
	}
	if !strings.HasPrefix(line, Sigil) || fenced(lines[:last]) {
		return tailLine{}, false
	}
	return tailLine{
		body: strings.TrimRight(strings.Join(lines[:last], "\n"), " \t\n"),
		rest: strings.TrimLeft(line[len(Sigil):], " \t"),
	}, true
}

func (t tailLine) options() []string {
	rest := t.rest
	braced := strings.HasPrefix(rest, string(openBrace))
	if braced {
		rest = rest[1:]
	}
	tokens := tokenize(rest)
	if braced && len(tokens) > 0 {
		last := &tokens[len(tokens)-1]
		if !last.quoted && strings.HasSuffix(last.text, string(closeBrace)) {
			last.text = strings.TrimSuffix(last.text, string(closeBrace))
		}
	}
	texts := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		texts = append(texts, tok.text)
	}
	return Dedupe(texts)
}

func Dedupe(options []string) []string {
	out := make([]string, 0, len(options))
	seen := make(map[string]bool, len(options))
	for _, opt := range options {
		if opt == "" || seen[opt] {
			continue
		}
		seen[opt] = true
		out = append(out, opt)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func Append(body string, options []string) string {
	quoted := make([]string, 0, len(options))
	seen := make(map[string]bool, len(options))
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		if opt == "" || seen[opt] {
			continue
		}
		seen[opt] = true
		quoted = append(quoted, quote(opt))
	}
	if len(quoted) == 0 {
		return body
	}
	line := Sigil + " " + strings.Join(quoted, " ")
	body = strings.TrimRight(body, " \t\n")
	if body == "" {
		return line
	}
	return body + "\n" + line
}

func Choices(s string) []string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "=")
	var parts []string
	var b strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			b.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ',':
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	parts = append(parts, strings.TrimSpace(b.String()))
	return Dedupe(parts)
}

func Ref(option string) (string, bool) {
	if !strings.HasPrefix(option, "@") {
		return "", false
	}
	key := strings.TrimSuffix(option[1:], "!")
	if strings.HasPrefix(key, "{") && strings.HasSuffix(key, "}") {
		key = key[1 : len(key)-1]
	}
	key = strings.TrimSpace(key)
	return key, key != ""
}

func YesNo(options []string) bool {
	if len(options) != 2 {
		return false
	}
	yes, no := false, false
	for _, opt := range options {
		switch strings.ToLower(strings.TrimSpace(opt)) {
		case "yes", "y":
			yes = true
		case "no", "n":
			no = true
		}
	}
	return yes && no
}

type token struct {
	text   string
	quoted bool
}

func tokenize(s string) []token {
	var out []token
	runes := []rune(s)
	for i := 0; i < len(runes); {
		for i < len(runes) && (runes[i] == ' ' || runes[i] == '\t') {
			i++
		}
		if i >= len(runes) {
			break
		}
		var b strings.Builder
		quoted := false
		for i < len(runes) && runes[i] != ' ' && runes[i] != '\t' {
			switch runes[i] {
			case '"', '\'':
				q := runes[i]
				quoted = true
				i++
				for i < len(runes) && runes[i] != q {
					if runes[i] == escape && i+1 < len(runes) {
						i++
					}
					b.WriteRune(runes[i])
					i++
				}
				if i < len(runes) {
					i++
				}
			case escape:
				i++
				if i < len(runes) {
					b.WriteRune(runes[i])
					i++
				}
			default:
				b.WriteRune(runes[i])
				i++
			}
		}
		out = append(out, token{text: b.String(), quoted: quoted})
	}
	return out
}

func quote(opt string) string {
	if !strings.ContainsAny(opt, " \t\"'{}\\") {
		return opt
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range opt {
		if r == '"' || r == escape {
			b.WriteRune(escape)
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func fenced(before []string) bool {
	open := false
	for _, line := range before {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			open = !open
		}
	}
	return open
}

func IsModel(model string) bool {
	name := model
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimPrefix(name, "~")
	return name == "jev" || strings.HasPrefix(name, "jev-")
}

const (
	APISystemOne = "systemone"
	APIOpenAI    = "openai"
)

func APIFor(model string) string {
	name := strings.ToLower(model)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if strings.HasPrefix(name, "gpt-") || strings.Contains(name, "luna") {
		return APIOpenAI
	}
	return APISystemOne
}

func ParseAPI(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "systemone", "system-one", "system_one", "jev":
		return APISystemOne, nil
	case "openai", "decisions":
		return APIOpenAI, nil
	}
	return "", fmt.Errorf("decision API %q: want %s or %s", s, APISystemOne, APIOpenAI)
}
