package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func getFixture(t *testing.T) form.Snapshot {
	t.Helper()
	keys := map[string]json.RawMessage{
		"system.cwd":   json.RawMessage(`"/home/someone/dev"`),
		"system.ttl":   json.RawMessage(`3600`),
		"system.quiet": json.RawMessage(`true`),
		"note":         json.RawMessage(`null`),
		"comments":     json.RawMessage(`{"p24":{"thread":"open","n":2}}`),
		"tags":         json.RawMessage(`["a","b"]`),
	}
	return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, keys, nil))
}

func got(t *testing.T, snap form.Snapshot, asJSON bool, paths ...string) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := writeGot(&b, snap, paths, asJSON)
	return b.String(), err
}

func TestGetPrintsAStringBareAndTheRestAsJSON(t *testing.T) {
	snap := getFixture(t)
	for _, tc := range []struct{ path, want string }{
		{"system.cwd", "/home/someone/dev\n"},
		{"system.ttl", "3600\n"},
		{"system.quiet", "true\n"},
		{"note", "null\n"},
		{"comments.p24.thread", "open\n"},
		{"comments.p24", "{\n  \"n\": 2,\n  \"thread\": \"open\"\n}\n"},
		{"tags", "[\n  \"a\",\n  \"b\"\n]\n"},
		{`comments["p24"].n`, "2\n"},
	} {
		out, err := got(t, snap, false, tc.path)
		require.NoError(t, err, tc.path)
		require.Equal(t, tc.want, out, tc.path)
	}
}

func TestGetJSONQuotesAString(t *testing.T) {
	out, err := got(t, getFixture(t), true, "system.cwd")
	require.NoError(t, err)
	require.Equal(t, "\"/home/someone/dev\"\n", out)
}

func TestGetSeveralPathsPrintInOrder(t *testing.T) {
	snap := getFixture(t)
	out, err := got(t, snap, false, "system.ttl", "system.cwd")
	require.NoError(t, err)
	require.Equal(t, "3600\n/home/someone/dev\n", out)

	out, err = got(t, snap, true, "system.ttl", "system.cwd")
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &obj))
	require.Equal(t, map[string]any{"system.ttl": float64(3600), "system.cwd": "/home/someone/dev"}, obj)
	require.Less(t, strings.Index(out, "system.ttl"), strings.Index(out, "system.cwd"), "the object keeps the order asked")
}

func TestGetAMissingPathIsAnErrorNamingIt(t *testing.T) {
	snap := getFixture(t)
	out, err := got(t, snap, false, "system.cwd", "nope.deeper", "comments.p99")
	require.Equal(t, "/home/someone/dev\n", out, "the paths that exist still print")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no key nope.deeper")
	require.Contains(t, err.Error(), "no key comments.p99")

	_, err = got(t, snap, false, "system.cwd.more")
	require.EqualError(t, err, "no key system.cwd.more")
}

func TestALeadingDotIsGet(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want []string
	}{
		{[]string{".system.cwd"}, []string{"get", "system.cwd"}},
		{[]string{".system.cwd", "-j"}, []string{"get", "system.cwd", "-j"}},
		{[]string{".a", ".b", "--id", "x"}, []string{"get", "a", "b", "--id", "x"}},
		{[]string{"show", ".system.cwd"}, []string{"get", "system.cwd"}},
		{[]string{"show", "--id", "abc", ".mantra", "-j"}, []string{"get", "--id", "abc", "mantra", "-j"}},
		{[]string{"history", ".mantra"}, []string{"get", "mantra"}},
		{[]string{"show", "abc", "-n", "5"}, []string{"show", "abc", "-n", "5"}},
		{[]string{"show", "--from", "4"}, []string{"show", "--from", "4"}},
		{[]string{"send", "-f", "--", ".not.a.path"}, []string{"send", "-f", "--", ".not.a.path"}},
		{[]string{"set", ".x", "1"}, []string{"set", ".x", "1"}},
		{[]string{"cd", "../x"}, []string{"cd", "../x"}},
		{[]string{"./script"}, []string{"./script"}},
	} {
		out, err := dotGetArgs(tc.in)
		require.NoError(t, err, "%v", tc.in)
		require.Equal(t, tc.want, out, "%v", tc.in)
	}
}

func TestALeadingDotRefusesWhatItCannotMean(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{".5"}, "LT coordinate"},
		{[]string{"show", ".12"}, "LT coordinate"},
		{[]string{"show", ".mantra", "-n", "5"}, "takes only --id and -j"},
		{[]string{".mantra", "system.cwd"}, "leading dot"},
	} {
		_, err := dotGetArgs(tc.in)
		require.Error(t, err, "%v", tc.in)
		require.Contains(t, err.Error(), tc.want, "%v", tc.in)
	}
}
