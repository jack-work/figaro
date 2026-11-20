package cli

import (
	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/internal/provider"
)

func fenceLogTurns(turns []aria.Turn) {
	for i := range turns {
		turns[i].Inquiry = provider.NeutraliseReservedTags(turns[i].Inquiry)
		segs := make([]aria.InquirySegment, len(turns[i].InquirySegments))
		for j, s := range turns[i].InquirySegments {
			s.Text = provider.NeutraliseReservedTags(s.Text)
			segs[j] = s
		}
		turns[i].InquirySegments = segs
	}
}

func fenceLogEntries(entries []irEntry) {
	for i := range entries {
		content := append(entries[i].Payload.Content[:0:0], entries[i].Payload.Content...)
		for j := range content {
			content[j].Text = provider.NeutraliseReservedTags(content[j].Text)
		}
		entries[i].Payload.Content = content
	}
}
