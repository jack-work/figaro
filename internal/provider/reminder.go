package provider

import (
	"regexp"
	"strings"
)

var reservedTag = regexp.MustCompile(`(?i)<(\s*/?\s*(?:system[\s_-]*reminder|laws?)\b)`)

func NeutraliseReservedTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	return reservedTag.ReplaceAllString(s, "&lt;$1")
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
