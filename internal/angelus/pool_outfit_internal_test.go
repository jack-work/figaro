package angelus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
)

const guestOutfit = `[system]
provider = "rou2"
model = "explabs/gpt-6.1-sol"
tools = ["say", "search"]

[system.retirement]
context_threshold = "50%"
action = "succeed"
ttl = "0"
`

var guestPool = config.PoolConfig{Outfit: "broker-group", Provider: "rou2"}

func outfitDir(t *testing.T, files map[string]string) *outfit.Outfitter {
	t.Helper()
	return outfitIn(t, t.TempDir(), files)
}

func outfitIn(t *testing.T, dir string, files map[string]string) *outfit.Outfitter {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return outfit.New(dir)
}

func withSystem(line string) string {
	return strings.Replace(guestOutfit, "[system]\n", "[system]\n"+line+"\n", 1)
}

// A guest steers this figaro, so its form may hold no host path, no skill, no
// second model to ask, no form to watch and no tool beyond the pool's two.
// Each row is one such thing alone in an outfit that otherwise pins.
func TestAPoolOutfitIsRefusedForWhatAGuestMustNotReach(t *testing.T) {
	if _, err := pinPoolOutfit(outfitDir(t, map[string]string{"outfits/broker-group.toml": guestOutfit}), guestPool); err != nil {
		t.Fatalf("the outfit every row is built from does not pin, so no row proves its fence: %v", err)
	}
	for name, c := range map[string]struct {
		body, want string
		files      map[string]string
	}{
		"a layer":                    {body: "layers = [\"base\"]\n" + guestOutfit, want: "has layers", files: map[string]string{"outfits/base.toml": "[system]\nmodel = \"m\"\n"}},
		"a skill":                    {body: guestOutfit + "\n[skills.search]\ncontent = \"how to search\"\n", want: "carries skills"},
		"a skills directory":         {body: "skills = { dirName = \"skills\" }\n" + guestOutfit, want: "carries skills", files: map[string]string{"skills/search.md": "---\nname: search\ndescription: d\n---\nbody\n"}},
		"a credo read from a file":   {body: withSystem("credo = { fileName = \"credo.md\" }"), want: "filePath", files: map[string]string{"credo.md": "be brief"}},
		"a path in a list of tables": {body: guestOutfit + "\n[[notes]]\nfilePath = \"/etc/hostname\"\n", want: "notes[].filePath"},
		"a working directory":        {body: withSystem("cwd = \"/home/gluck\""), want: "carries system.cwd"},
		"a wire dump directory":      {body: guestOutfit + "\n[system.environment]\nfigaro_wire_dir = \"/tmp/wire\"\n", want: "carries system.environment"},
		"methods":                    {body: guestOutfit + "\n[system.methods]\nenabled = true\n", want: "carries system.methods"},
		"a decision model":           {body: guestOutfit + "\n[system.decide]\nmodel = \"m\"\n", want: "carries system.decide"},
		"a study":                    {body: withSystem("studies = [\"@96447061\"]"), want: "carries system.studies"},
		"a ramp file":                {body: guestOutfit + "ramp = \"ramp.md\"\n", want: "carries system.retirement.ramp"},
		"its own page":               {body: guestOutfit + "page = \"@owner\"\n", want: "carries system.retirement.page"},
		"its own sandbox":            {body: withSystem("sandbox = \"local\""), want: "carries system.sandbox"},
		"its own scopes":             {body: withSystem("scopes = [\"*:*\"]"), want: "carries system.scopes"},
		"a tool that executes":       {body: strings.Replace(guestOutfit, `"search"]`, `"search", "bash"]`, 1), want: "lists the tool bash"},
		"no tools list":              {body: strings.Replace(guestOutfit, "tools = [\"say\", \"search\"]\n", "", 1), want: "no system.tools list"},
		"a tools value of one name":  {body: strings.Replace(guestOutfit, `["say", "search"]`, `"say"`, 1), want: "no system.tools list"},
		"another provider":           {body: strings.Replace(guestOutfit, `provider = "rou2"`, `provider = "anthropic"`, 1), want: "its pool's provider is"},
	} {
		files := map[string]string{"outfits/broker-group.toml": c.body}
		for k, v := range c.files {
			files[k] = v
		}
		_, err := pinPoolOutfit(outfitDir(t, files), guestPool)
		if err == nil {
			t.Errorf("%s: pinned", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused for another reason than %q: %v", name, c.want, err)
		}
	}
	if _, err := pinPoolOutfit(outfitDir(t, nil), guestPool); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("an outfit with no file: %v", err)
	}
	for name, tools := range map[string]string{"one of the two": `["say"]`, "neither": `[]`} {
		body := strings.Replace(guestOutfit, `["say", "search"]`, tools, 1)
		if _, err := pinPoolOutfit(outfitDir(t, map[string]string{"outfits/broker-group.toml": body}), guestPool); err != nil {
			t.Errorf("%s of the pool's tools: %v", name, err)
		}
	}
}

// A lease mints the outfit that was checked when its pool loaded. A file
// edited since then is a different outfit until a reload checks it.
func TestAnOutfitEditedAfterItsPoolLoadedIsNotTheOnePinned(t *testing.T) {
	dir := t.TempDir()
	ofit := outfitIn(t, dir, map[string]string{"outfits/broker-group.toml": guestOutfit})
	pinned, err := pinPoolOutfit(ofit, guestPool)
	if err != nil {
		t.Fatal(err)
	}
	if err := pinned.foldsAsPinned(ofit, guestPool); err != nil {
		t.Fatalf("an untouched outfit no longer folds as pinned: %v", err)
	}
	again, err := pinPoolOutfit(ofit, guestPool)
	if err != nil || again.hash != pinned.hash {
		t.Fatalf("the same file pinned twice gives %q then %q (%v)", pinned.hash, again.hash, err)
	}
	edited := strings.Replace(guestOutfit, "explabs/gpt-6.1-sol", "explabs/another", 1)
	if err := os.WriteFile(filepath.Join(dir, "outfits", "broker-group.toml"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	ofit.Reload()
	if err := pinned.foldsAsPinned(ofit, guestPool); err == nil || !strings.Contains(err.Error(), "changed since its pool loaded") {
		t.Fatalf("an edited outfit still folds as the pinned one: %v", err)
	}
}
