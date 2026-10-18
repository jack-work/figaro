package methods

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const varInquiry = "inquiry"

var placeholder = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

type Template struct {
	text  []string
	backs []int
}

func Parse(src string) (Template, error) {
	if strings.TrimSpace(src) == "" {
		return Template{}, errors.New("empty")
	}
	var t Template
	at := 0
	for _, loc := range placeholder.FindAllStringSubmatchIndex(src, -1) {
		back, err := inquiryBack(src[loc[2]:loc[3]])
		if err != nil {
			return Template{}, err
		}
		t.text = append(t.text, src[at:loc[0]])
		t.backs = append(t.backs, back)
		at = loc[1]
	}
	t.text = append(t.text, src[at:])
	return t, nil
}

func inquiryBack(name string) (int, error) {
	base, back, relative := strings.Cut(name, "~")
	if base != varInquiry {
		return 0, fmt.Errorf("{{%s}}: the only variable is %s, or %s~N for the Nth before it", name, varInquiry, varInquiry)
	}
	if !relative {
		return 0, nil
	}
	n, err := strconv.Atoi(back)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("{{%s}}: ~ takes a count of inquiries back from this one", name)
	}
	return n, nil
}

func (t Template) Depth() int {
	depth := 0
	for _, b := range t.backs {
		depth = max(depth, b)
	}
	return depth
}

func (t Template) Render(inquiries []string) string {
	var b strings.Builder
	for i, back := range t.backs {
		b.WriteString(t.text[i])
		if back < len(inquiries) {
			b.WriteString(inquiries[back])
		}
	}
	b.WriteString(t.text[len(t.text)-1])
	return b.String()
}
