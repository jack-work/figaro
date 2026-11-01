package angelus

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// Open keys (outside system.) are a box's to write unless held. Each one the
// daemon reads is classified here; a key read tomorrow and not classified fails
// this test, so the next reader decides whether a box may write it. Values
// under system. need no entry: a box writes only the system keys boxWritable
// lists.
var boxInertOpenKeys = map[string]string{
	"mantra":               "a title, shown and sent as text",
	"name":                 "a display name in the listing",
	"duke-title":           "who the end user is called, rendered as text",
	"anthropic":            "a provider registry name, not a board key",
	"decision":             "a key of a decision response, not a board key",
	"entries":              "a key of the daemon's ledger form, which no aria writes",
	"figaro_id":            "an rpc param, not a board key",
	"x-internal-figaro-id": "an rpc param, not a board key",
	"x-caller":             "an rpc param, not a board key",
}

var (
	readCall  = regexp.MustCompile(`\.(?:Lookup|Get)\("([a-z][a-z0-9_.-]*)"\)|\b(?:cbStr|cbInt|formStr|formString|formInt|snapshotString|snapshotInt|patchString|boundFormString)\([^)"]*"([a-z][a-z0-9_.-]*)"`)
	keyConst  = regexp.MustCompile(`\b\w+Key\s+=\s+"([a-z][a-z0-9_.-]*)"`)
	readRoots = []string{".", "../figaro", "../provider", "../decide", "../methods", "../store", "../outfit", "../sandbox", "../tool", "../daemon", "../modelcat", "../../api"}
)

func daemonReadKeys(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, root := range readRoots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, re := range []*regexp.Regexp{readCall, keyConst} {
				for _, m := range re.FindAllStringSubmatch(string(src), -1) {
					for _, k := range m[1:] {
						if k != "" {
							found[k] = path
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

func TestEveryOpenKeyTheDaemonReadsIsClassifiedForBoxes(t *testing.T) {
	found := daemonReadKeys(t)
	if _, ok := found["system.environment.figaro_wire_dir"]; !ok {
		t.Fatal("the scan missed a key it must see; it proves nothing")
	}
	for key, where := range found {
		if strings.HasPrefix(key, "system.") || key == "system" {
			continue
		}
		if _, refused := boxRefusal(key); refused {
			continue
		}
		if _, ok := boxInertOpenKeys[key]; !ok {
			t.Errorf("%s (read in %s) is not classified: hold it in boxHeldKeys or say why it is inert in boxInertOpenKeys", key, where)
		}
	}
}

func TestHeldKeysAreRefusedAndListedKeysAreNot(t *testing.T) {
	for _, k := range boxHeldKeys {
		if _, refused := boxRefusal(k.key); !refused {
			t.Errorf("%s is held but writable", k.key)
		}
		for _, w := range boxWritable {
			if under(k.key, w) || under(w, k.key) {
				t.Errorf("%s is both held and listed under %s", k.key, w)
			}
		}
	}
	for _, w := range boxWritable {
		if _, refused := boxRefusal(w); refused {
			t.Errorf("%s is listed but refused", w)
		}
	}
	if _, refused := boxRefusal("system.added_tomorrow"); !refused {
		t.Error("an unlisted system key is writable")
	}
}

// The chat outfit reaps its arias after a day idle; a box that could write its
// own lifetime could keep a guest's transcript forever.
func TestALifetimeIsNeverBoxWritable(t *testing.T) {
	for _, key := range []string{"system.ttl", "system.keepalive"} {
		for _, w := range boxWritable {
			if under(key, w) || under(w, key) {
				t.Errorf("%s is box-writable under %s", key, w)
			}
		}
		if _, refused := boxRefusal(key); !refused {
			t.Errorf("%s is not refused to a box", key)
		}
	}
}

func TestOwnerHeldKeysAreNeverBoxWritable(t *testing.T) {
	for _, key := range form.OwnerHeldKeys() {
		for _, w := range boxWritable {
			if under(key, w) || under(w, key) {
				t.Errorf("owner-held %s is box-writable under %s", key, w)
			}
		}
	}
}
