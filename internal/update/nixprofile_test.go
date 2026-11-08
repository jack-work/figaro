package update

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

const profileListJSON = `{"version":3,"elements":{
"cli":{"active":true,"attrPath":"packages.x86_64-linux.default","originalUrl":"github:googleworkspace/cli","storePaths":["/nix/store/sm5jigckzhb21jcx96yh64hg9kxg9f6b-gws-0.22.5"]},
"git+file:///home/gluck/dev/figaro-qua/.bare?ref=release#packages.x86_64-linux.default":{"active":true,"attrPath":"packages.x86_64-linux.default","originalUrl":"git+file:///home/gluck/dev/figaro-qua/.bare?ref=release","storePaths":["/nix/store/n0wjsn837xwbqak7pqi0nzwzrmch6a47-figaro-0.55.6"]},
"rclone":{"storePaths":["/nix/store/g4xjl0gk1y7j97ngjsg8wsrgs17i444w-rclone-1.75.2-man","/nix/store/wphsbgbfcf18ri4gci1dyj41cbpvqxkv-rclone-1.75.2"]}}}`

const figaroEntry = "git+file:///home/gluck/dev/figaro-qua/.bare?ref=release#packages.x86_64-linux.default"

// Gluck ran the hint `nix profile upgrade figaro` and nix matched nothing:
// a profile entry installed from a flake URL is named by that URL.
func TestUpgradeCommandNamesTheProfileEntryNixKnows(t *testing.T) {
	exe := "/nix/store/n0wjsn837xwbqak7pqi0nzwzrmch6a47-figaro-0.55.6/bin/figaro"
	if got := NixProfileEntry(exe, []byte(profileListJSON)); got != figaroEntry {
		t.Fatalf("entry for %s: got %q, want %q", exe, got, figaroEntry)
	}
	for _, other := range []string{"/nix/store/zzz-figaro-0.55.5/bin/figaro", "/home/g/go/bin/figaro"} {
		if got := NixProfileEntry(other, []byte(profileListJSON)); got != "" {
			t.Errorf("%s is in no element, got %q", other, got)
		}
	}
	if got := NixProfileEntry(exe, []byte(`{"version":2,"elements":[{"storePaths":["/nix/store/n0wjsn837xwbqak7pqi0nzwzrmch6a47-figaro-0.55.6"]}]}`)); got != "" {
		t.Errorf("a list with no names names nothing, got %q", got)
	}

	cmd := UpgradeCommand(&Info{Channel: ChannelNix, NixEntry: figaroEntry}, "example.com/mod")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	out, err := exec.Command(sh, "-c", `set -f; set -- `+strings.TrimPrefix(cmd, "nix ")+`; printf '%s\n' "$@"`).Output()
	if err != nil {
		t.Fatalf("%q: %v", cmd, err)
	}
	if want := "profile\nupgrade\n" + figaroEntry + "\n"; string(out) != want {
		t.Errorf("%q reaches nix as %q, want %q", cmd, out, want)
	}

	generic := UpgradeCommand(&Info{Channel: ChannelNix}, "example.com/mod")
	if strings.Contains(generic, "upgrade figaro") {
		t.Errorf("with no entry found the hint must not name one that may not exist: %q", generic)
	}
}

func TestTheNixEntryIsReadFromNixProfileList(t *testing.T) {
	was := nixProfileList
	t.Cleanup(func() { nixProfileList = was })
	nixProfileList = func(context.Context) ([]byte, error) { return nil, errors.New("no nix") }
	if got := nixProfileEntry(context.Background(), "/nix/store/n0wjsn837xwbqak7pqi0nzwzrmch6a47-figaro-0.55.6/bin/figaro"); got != "" {
		t.Errorf("no nix, no entry: got %q", got)
	}
	nixProfileList = func(context.Context) ([]byte, error) { return []byte(profileListJSON), nil }
	if got := nixProfileEntry(context.Background(), "/nix/store/n0wjsn837xwbqak7pqi0nzwzrmch6a47-figaro-0.55.6/bin/figaro"); got != figaroEntry {
		t.Errorf("got %q", got)
	}
}
