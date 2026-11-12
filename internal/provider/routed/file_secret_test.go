package routed

import (
	"bytes"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// El Inspector XIV: a bad provider file is named by path and line, never by
// the decoder's message, which can quote a credential. Covers LoadFile's
// error and the warning RegisterDeclared logs when it skips the file.
const plantedToken = "skPLANTEDPRIVATETOKENVALUE"

var badProviderFiles = map[string]struct {
	doc, where string
}{
	"syntax":     {"preset = \"gateway\"\napi_key = " + plantedToken + "\n", "line 2"},
	"wrong type": {"preset = \"gateway\"\nmax_request_bytes = \"" + plantedToken + "\"\n", "is not valid"},
}

func TestABadProviderFileNeverQuotesItsValues(t *testing.T) {
	for kind, tc := range badProviderFiles {
		t.Run(kind, func(t *testing.T) {
			loaded := declaredDir(t, map[string]string{"leaky": tc.doc})
			path := filepath.Join(loaded.ConfigDir, "providers", "leaky.toml")

			_, err := LoadFile(loaded, "leaky")
			if err == nil {
				t.Fatal("a bad provider file must be an error")
			}
			if strings.Contains(err.Error(), plantedToken) {
				t.Fatalf("LoadFile quoted the token: %v", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.where) {
				t.Fatalf("err = %v; want the path and %q", err, tc.where)
			}

			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })
			if got := RegisterDeclared(loaded); len(got) != 0 {
				t.Fatalf("registered %v from a bad file", got)
			}
			if !strings.Contains(logs.String(), "provider file ignored") {
				t.Fatalf("no warning logged: %q", logs.String())
			}
			if strings.Contains(logs.String(), plantedToken) {
				t.Fatalf("the warning quoted the token: %s", logs.String())
			}
		})
	}
}

// Canary: the decoder's own message quotes the token in a syntax error, so
// wrapping it (as LoadFile once did) would fail the test above.
func TestTheDecoderMessageWouldLeakTheToken(t *testing.T) {
	var f File
	_, err := toml.Decode(badProviderFiles["syntax"].doc, &f)
	if err == nil {
		t.Fatal("the planted file must not decode")
	}
	if rewrapped := fmt.Errorf("%s: %w", "/p/leaky.toml", err); !strings.Contains(rewrapped.Error(), plantedToken) {
		t.Fatalf("rewrapped error %q does not carry the token; the test above has no teeth", rewrapped)
	}
}
