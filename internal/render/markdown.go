package render

import (
	"bytes"
	"strconv"
	"strings"

	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const (
	itemMark        = "\u2060"
	paragraphMark   = "\u2063"
	quoteMark       = "\u2064"
	listMarkerCells = 3
	quoteBar        = "│ "
	heldHyphen      = "\u2011"
)

var layoutMarks = strings.NewReplacer(itemMark, "", paragraphMark, "", quoteMark, "")

func stripLayoutMarks(row string) string {
	for _, mark := range []string{itemMark, paragraphMark, quoteMark} {
		for at := strings.Index(row, mark); at >= 0; at = strings.Index(row, mark) {
			from, to := at, at+len(mark)
			if open := strings.LastIndex(row[:at], "\x1b["); open >= 0 && isSGR(row[open:at]) &&
				strings.HasPrefix(row[to:], "\x1b[m") {
				from, to = open, to+len("\x1b[m")
			}
			row = row[:from] + row[to:]
		}
	}
	return row
}

func isSGR(seq string) bool {
	return len(seq) >= 3 && seq[len(seq)-1] == 'm' && strings.Trim(seq[2:len(seq)-1], "0123456789;") == ""
}

type markdownRenderer struct {
	md     goldmark.Markdown
	marker xansi.Style
	width  int
}

func newMarkdownRenderer(width int) *markdownRenderer {
	st := styles.DarkStyleConfig
	indent := uint(listMarkerCells)
	st.List.StyleBlock.Indent = &indent
	st.List.LevelIndent = listMarkerCells
	st.Item.BlockPrefix = itemMark
	st.Task.Ticked = itemMark
	st.Task.Unticked = itemMark
	st.Paragraph.Suffix = paragraphMark
	quoteIndent := uint(len([]rune(quoteBar)))
	st.BlockQuote.Indent = &quoteIndent
	quoteToken := quoteMark
	st.BlockQuote.IndentToken = &quoteToken
	tableWrap := true
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	md.SetRenderer(renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(
		ansi.NewRenderer(ansi.Options{WordWrap: max(width+2, 1), TableWrap: &tableWrap, Styles: st}), 1000))))
	var marker xansi.Style
	if c := st.Document.Color; c != nil {
		marker = marker.ForegroundColor(lipgloss.Color(*c))
	}
	return &markdownRenderer{md: md, marker: marker, width: width}
}

func (r *markdownRenderer) Render(md string) (string, error) {
	source := r.holdHyphens([]byte(layoutMarks.Replace(md)))
	doc := r.md.Parser().Parse(text.NewReader(source))
	layout := layOutLists(doc)
	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, source, doc); err != nil {
		return "", err
	}
	return layout.apply(buf.String(), r.marker, r.width), nil
}

func (r *markdownRenderer) holdHyphens(source []byte) []byte {
	if !bytes.Contains(source, []byte("-")) {
		return source
	}
	doc := r.md.Parser().Parse(text.NewReader(source))
	held := make([]bool, len(source))
	_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch n.Kind() {
		case gast.KindParagraph, gast.KindTextBlock, gast.KindHeading:
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				for k := seg.Start; k < seg.Stop; k++ {
					held[k] = true
				}
			}
		case gast.KindAutoLink:
			label := n.(*gast.AutoLink).Label(source)
			at := cap(source) - cap(label)
			for k := at; k >= 0 && k < at+urlPathStart(label) && k < len(held); k++ {
				held[k] = false
			}
		}
		return gast.WalkContinue, nil
	})
	out := make([]byte, 0, len(source)+16)
	for k, b := range source {
		if b == '-' && held[k] {
			out = append(out, heldHyphen...)
			continue
		}
		out = append(out, b)
	}
	return out
}

func urlPathStart(url []byte) int {
	host := 0
	if i := bytes.Index(url, []byte("://")); i >= 0 {
		host = i + 3
	}
	if i := bytes.IndexByte(url[host:], '/'); i >= 0 {
		return host + i
	}
	return len(url)
}

type listLayout struct {
	markers []string
	keepGap []bool
}

func layOutLists(doc gast.Node) listLayout {
	var items []*gast.ListItem
	_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if item, ok := n.(*gast.ListItem); ok && entering {
			items = append(items, item)
		}
		return gast.WalkContinue, nil
	})
	var l listLayout
	for _, item := range items {
		l.markers = append(l.markers, itemMarker(item))
		hoistParagraphs(item)
	}
	for _, item := range items {
		if list, ok := item.Parent().(*gast.List); ok {
			list.Marker = '-'
		}
	}
	_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if n.Kind() == gast.KindParagraph && entering {
			l.keepGap = append(l.keepGap, !endsItemText(n))
		}
		return gast.WalkContinue, nil
	})
	return l
}

func itemMarker(item *gast.ListItem) string {
	if text := item.FirstChild(); text != nil {
		if box, ok := text.FirstChild().(*east.TaskCheckBox); ok {
			if box.IsChecked {
				return "[x]"
			}
			return "[ ]"
		}
	}
	list, ok := item.Parent().(*gast.List)
	if !ok || !list.IsOrdered() {
		return "•"
	}
	n := list.Start
	for s := item.PreviousSibling(); s != nil; s = s.PreviousSibling() {
		n++
	}
	return strconv.Itoa(n) + string(list.Marker)
}

func hoistParagraphs(item *gast.ListItem) {
	for c := item.FirstChild(); c != nil; {
		next := c.NextSibling()
		if k := c.Kind(); k != gast.KindTextBlock && k != gast.KindParagraph {
			c = next
			continue
		}
		follows := c.PreviousSibling() != nil
		p := gast.NewParagraph()
		p.SetLines(c.Lines())
		for in := c.FirstChild(); in != nil; {
			after := in.NextSibling()
			p.AppendChild(p, in)
			in = after
		}
		if box, ok := p.FirstChild().(*east.TaskCheckBox); ok {
			holder := gast.NewTextBlock()
			holder.AppendChild(holder, box)
			item.InsertBefore(item, c, holder)
		}
		wrapper := gast.NewTextBlock()
		if follows {
			wrapper.AppendChild(wrapper, gast.NewTextBlock())
		}
		wrapper.AppendChild(wrapper, p)
		item.ReplaceChild(item, c, wrapper)
		c = next
	}
}

func endsItemText(p gast.Node) bool {
	wrapper := p.Parent()
	if wrapper == nil || wrapper.Kind() != gast.KindTextBlock || wrapper.Parent() == nil ||
		wrapper.Parent().Kind() != gast.KindListItem {
		return false
	}
	next := wrapper.NextSibling()
	return next == nil || next.Kind() == gast.KindList
}

func (l listLayout) apply(out string, marker xansi.Style, width int) string {
	rows := strings.Split(out, "\n")
	kept := rows[:0]
	item, para := 0, 0
	for _, row := range rows {
		gapEnds := false
		for n := strings.Count(row, paragraphMark); n > 0; n-- {
			if para < len(l.keepGap) && !l.keepGap[para] {
				gapEnds = true
			}
			para++
		}
		if barred := drawQuoteBars(row); barred != row {
			row = xansi.Truncate(barred, max(width, 1), "")
		}
		if gapEnds && onlyRules(layoutMarks.Replace(row)) {
			continue
		}
		if n := strings.Count(row, itemMark); n > 0 && item < len(l.markers) {
			row = placeMarker(row, l.markers[item], marker)
			item += n
		}
		kept = append(kept, strings.ReplaceAll(stripLayoutMarks(row), heldHyphen, "-"))
	}
	return strings.Join(kept, "\n")
}

func placeMarker(row, marker string, style xansi.Style) string {
	at := strings.Index(row, itemMark)
	col := xansi.StringWidth(row[:at])
	label := marker + " "
	from := max(col-xansi.StringWidth(label), 0)
	return xansi.Truncate(row, from, "") + style.Styled(label) + xansi.TruncateLeft(row, col, "")
}

func onlyRules(row string) bool {
	return strings.Trim(StripEscapes(row), " │") == ""
}

func drawQuoteBars(row string) string {
	if !strings.Contains(row, quoteMark) {
		return row
	}
	bar := []rune(quoteBar)
	var b strings.Builder
	n := 0
	for {
		at := strings.Index(row, quoteMark)
		if at < 0 {
			break
		}
		b.WriteString(row[:at])
		b.WriteRune(bar[n%len(bar)])
		n++
		row = row[at+len(quoteMark):]
	}
	b.WriteString(row)
	return b.String()
}
