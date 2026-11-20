package cli

import (
	"bytes"
	"io"
	"regexp"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/internal/provider"
)

var reservedOpening = regexp.MustCompile(`(?i)(?:<|&lt;?|&#0*60;?|&#x0*3c;?)(\s*/?\s*(?:system[\s_-]*reminder|laws?)\b)`)

var logANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

func disarmLogText(s string) string {
	return reservedOpening.ReplaceAllString(s, "‹$1")
}

func disarmLogTurns(turns []aria.Turn) {
	for i := range turns {
		turns[i].Inquiry = disarmLogText(turns[i].Inquiry)
		segs := make([]aria.InquirySegment, len(turns[i].InquirySegments))
		for j, s := range turns[i].InquirySegments {
			s.Text = disarmLogText(s.Text)
			segs[j] = s
		}
		turns[i].InquirySegments = segs
	}
}

func disarmLogEntries(entries []irEntry) {
	for i := range entries {
		content := append(entries[i].Payload.Content[:0:0], entries[i].Payload.Content...)
		for j := range content {
			content[j].Text = disarmLogText(content[j].Text)
		}
		entries[i].Payload.Content = content
	}
}

func fenceLine(line string) string {
	line = provider.NeutraliseReservedTags(line)
	if !bytes.ContainsRune([]byte(line), 0x1b) {
		return line
	}
	var plain []byte
	var at []int
	for i := 0; i < len(line); {
		if loc := logANSI.FindStringIndex(line[i:]); loc != nil && loc[0] == 0 {
			i += loc[1]
			continue
		}
		plain = append(plain, line[i])
		at = append(at, i)
		i++
	}
	matches := reservedOpening.FindAllIndex(plain, -1)
	if len(matches) == 0 {
		return line
	}
	var b []byte
	last := 0
	for _, m := range matches {
		start, end := at[m[0]], at[m[1]-1]+1
		b = append(b, line[last:start]...)
		b = append(b, disarmLogText(logANSI.ReplaceAllString(line[start:end], ""))...)
		last = end
	}
	return string(append(b, line[last:]...))
}

type fenceWriter struct {
	w   io.Writer
	buf []byte
}

func (f *fenceWriter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	cut := bytes.LastIndexByte(f.buf, '\n')
	if cut < 0 {
		return len(p), nil
	}
	if _, err := io.WriteString(f.w, fenceLine(string(f.buf[:cut+1]))); err != nil {
		return 0, err
	}
	f.buf = append(f.buf[:0], f.buf[cut+1:]...)
	return len(p), nil
}

func (f *fenceWriter) Flush() error {
	if len(f.buf) == 0 {
		return nil
	}
	_, err := io.WriteString(f.w, fenceLine(string(f.buf)))
	f.buf = f.buf[:0]
	return err
}

func fenceStdout() func() {
	prev := stdout
	fw := &fenceWriter{w: prev}
	stdout = fw
	return func() {
		_ = fw.Flush()
		stdout = prev
	}
}
