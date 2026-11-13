package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const poolBody = `[authz]
policy = "policy.toml"

[slot.pools.chat-groups]
enabled = true
outfit = "broker-group"
provider = "rou2"
rou2_lease_class = "chat-group"
say_url = "https://broker.example/groups/say"
slots = ["slot0", "slot1"]
`

func loadBody(t *testing.T, body string, providerFiles ...string) (*Loaded, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range providerFiles {
		if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "providers", name+".toml"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(dir)
}

func TestAPoolLoadsWithItsDefaults(t *testing.T) {
	l, err := loadBody(t, poolBody, "rou2")
	if err != nil {
		t.Fatal(err)
	}
	if !l.PoolDaemon() {
		t.Fatal("a config with a pool is not a pool daemon's")
	}
	if got := l.Pools(); len(got) != 1 || got[0] != "chat-groups" {
		t.Fatalf("pools %v", got)
	}
	p, ok := l.Pool("chat-groups")
	if !ok || !p.Enabled || p.Outfit != "broker-group" || p.Provider != "rou2" {
		t.Fatalf("pool %+v %v", p, ok)
	}
	if got := p.SlotNames(); len(got) != 2 || got[0] != "slot0" || got[1] != "slot1" {
		t.Fatalf("slots %v", got)
	}
	if p.SuccessionBound() != 4 || p.DeliverCap() != 16<<10 || p.SayBound() != 20 {
		t.Fatalf("defaults: %d successions a day, %d bytes a deliver, %d says a minute", p.SuccessionBound(), p.DeliverCap(), p.SayBound())
	}
	if _, ok := l.Pool("other"); ok {
		t.Fatal("a pool nobody declared was found")
	}
}

// A config with no pool is every daemon there was before pools: it must load
// as it did, and it is not a pool daemon.
func TestAConfigWithoutAPoolIsNotAPoolDaemons(t *testing.T) {
	l, err := loadBody(t, "[sandbox.boxes.slot3-g7]\nexec = \"/run/box/a.sock\"\n[telemetry]\nlevel = \"debug\"\n", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if l.PoolDaemon() || len(l.Pools()) != 0 {
		t.Fatal("a config with no pool reads as a pool daemon's")
	}
}

// Absent means off, and a pool that is off still makes the daemon a pool
// daemon: turning a pool off is the kill switch, and it must not lift the
// refusals with it.
func TestAPoolIsOffUnlessToldAndStaysAPoolDaemonWhenOff(t *testing.T) {
	body := strings.Replace(poolBody, "enabled = true\n", "", 1)
	l, err := loadBody(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := l.Pool("chat-groups"); p.Enabled {
		t.Fatal("a pool that never said enabled is on")
	}
	if !l.PoolDaemon() {
		t.Fatal("a pool turned off stopped the daemon being a pool daemon")
	}
	if _, err := loadBody(t, body+"[troupe]\nname = \"kelliher\"\n"); err == nil {
		t.Fatal("a pool turned off let a troupe in")
	}
}

func TestAPoolRefusesWhatItCannotBe(t *testing.T) {
	swap := func(old, new string) string {
		if !strings.Contains(poolBody, old) {
			t.Fatalf("the base config has no %q to replace", old)
		}
		return strings.Replace(poolBody, old, new, 1)
	}
	slots := `slots = ["slot0", "slot1"]` + "\n"
	for name, c := range map[string]struct{ body, want string }{
		"a slot with an exec path":    {swap(slots, `slots = ["slot0", {name = "slot1", exec = "/run/box/slot1.sock"}]`+"\n"), "carrying exec"},
		"a slot with a door":          {swap(slots, `slots = [{name = "slot0", door = "/run/door/slot0.sock"}]`+"\n"), "carrying door"},
		"a slot with a reset":         {swap(slots, `slots = [{name = "slot0", reset = "/run/reset/slot0"}]`+"\n"), "carrying name, reset"},
		"slots as tables":             {swap(slots, "[slot.pools.chat-groups.slots.slot0]\nexec = \"/run/box/slot0.sock\"\n"), "incompatible types"},
		"a slot that is a number":     {swap(slots, "slots = [3]\n"), "a slot is a name"},
		"no slots":                    {swap(slots, ""), "can lease nothing"},
		"an exec path on the pool":    {poolBody + "exec = \"/run/box/pool.sock\"\n", "slot.pools.chat-groups.exec"},
		"a misspelt enabled":          {swap("enabled = true", "enabeld = false"), "slot.pools.chat-groups.enabeld"},
		"an unknown key under slot":   {poolBody + "[slot.reset]\ncommand = \"/bin/reset\"\n", "slot.reset"},
		"no outfit":                   {swap("outfit = \"broker-group\"\n", ""), "outfit"},
		"the outfit named default":    {swap(`"broker-group"`, `"default"`), "daemon's own figaros"},
		"the config's default outfit": {"default_outfit = \"house\"\n" + swap(`"broker-group"`, `"house"`), "daemon's own figaros"},
		"no provider":                 {swap("provider = \"rou2\"\n", ""), "provider"},
		"no lease class":              {swap("rou2_lease_class = \"chat-group\"\n", ""), "rou2_lease_class"},
		"a route of broker's":         {poolBody + "page_route = \"admin\"\n", "slot.pools.chat-groups.page_route: not a key figaro knows"},
		"a figaro to page":            {poolBody + "page = \"@owner\"\n", "slot.pools.chat-groups.page: not a key figaro knows"},
		"no say url":                  {swap("say_url = \"https://broker.example/groups/say\"\n", ""), "say_url"},
		"a say url with no scheme":    {swap("https://broker.example/groups/say", "broker.example/groups/say"), "say_url"},
		"a say url with a password":   {swap("https://broker.example", "https://u:p@broker.example"), "say_url"},
		"a say url on a file":         {swap("https://broker.example/groups/say", "file:///etc/passwd"), "say_url"},
		"a slot named like a lease":   {swap(`"slot1"`, `"slot1-g7"`), "ends like a lease"},
		"a slot with a colon":         {swap(`"slot1"`, `"slot:1"`), "slot name"},
		"an empty slot name":          {swap(`"slot1"`, `""`), "slot name"},
		"a slot named self":           {swap(`"slot1"`, `"self"`), "reserved"},
		"one slot twice":              {swap(`"slot1"`, `"slot0"`), "also a slot of"},
		"one slot in two pools":       {poolBody + strings.Replace(poolBody[strings.Index(poolBody, "[slot.pools"):], "chat-groups", "other", 1), "also a slot of"},
		"a pool name with a slash":    {swap("[slot.pools.chat-groups]", `[slot.pools."chat/groups"]`), "pool name"},
		"negative successions":        {poolBody + "successions_per_day = -1\n", "successions_per_day"},
		"a negative deliver cap":      {poolBody + "deliver_max_bytes = -1\n", "deliver_max_bytes"},
		"a negative say bound":        {poolBody + "say_per_minute = -1\n", "say_per_minute"},
		"a search credential by path": {poolBody + "search_credential = \"/etc/search.key\"\n", "search_credential"},
		"a search credential of dots": {poolBody + "search_credential = \"..\"\n", "search_credential"},
	} {
		_, err := loadBody(t, c.body, "rou2")
		if err == nil {
			t.Errorf("%s: loaded", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused for another reason than %q: %v", name, c.want, err)
		}
	}
}

func TestAPoolTakesTheKeysItKnows(t *testing.T) {
	l, err := loadBody(t, poolBody+"successions_per_day = 0\ndeliver_max_bytes = 4096\nsay_per_minute = 3\nsearch_credential = \"search-groups\"\n", "rou2")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := l.Pool("chat-groups")
	if p.SuccessionBound() != 0 || p.DeliverCap() != 4096 || p.SayBound() != 3 || p.SearchCredential != "search-groups" {
		t.Fatalf("pool %+v", p)
	}
}

// Each thing alone, beside an otherwise good pool. The reason is checked so a
// snippet that fails some other validation cannot pass for the fence.
func TestAPoolDaemonRefusesWhatItCannotHold(t *testing.T) {
	const beside = "beside [slot.pools]"
	for name, c := range map[string]struct {
		body      string
		providers []string
		want      string
	}{
		"a troupe":          {poolBody + "[troupe]\nname = \"kelliher\"\n", nil, "[troupe] " + beside},
		"a peer":            {poolBody + "[peers.gluck]\ndoor = \"gluck:7777\"\n", nil, "[peers] " + beside},
		"a node door":       {poolBody + "[door.tls]\nlisten = \":7777\"\n", nil, "[door.tls] " + beside},
		"a static box":      {poolBody + "[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n", nil, "[sandbox.boxes] " + beside},
		"a service token":   {poolBody + "[service_tokens.edge]\nissuer = \"https://auth.example\"\n", nil, "[service_tokens] " + beside},
		"an api key":        {strings.Replace(poolBody, "policy = \"policy.toml\"\n", "policy = \"policy.toml\"\n[[authz.api_keys]]\nid = \"k1\"\n", 1), nil, "[authz.api_keys] " + beside},
		"a uid map":         {strings.Replace(poolBody, "policy = \"policy.toml\"\n", "policy = \"policy.toml\"\n[authz.peercred.uids]\n1001 = \"user:guest\"\n", 1), nil, "[authz.peercred] " + beside},
		"no trunks":         {"trunks = false\n" + poolBody, nil, "trunks = false " + beside},
		"a debug level":     {"[telemetry]\nlevel = \"Debug\"\n" + poolBody, nil, "level"},
		"no policy named":   {poolBody[strings.Index(poolBody, "[slot.pools"):], nil, "never falls back to the default policy"},
		"a stray provider":  {poolBody, []string{"rou2", "anthropic"}, "providers/anthropic.toml " + beside},
		"only a stray file": {poolBody, []string{"copilot"}, "providers/copilot.toml " + beside},
	} {
		_, err := loadBody(t, c.body, c.providers...)
		if err == nil {
			t.Errorf("%s: loaded", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused for another reason than %q: %v", name, c.want, err)
		}
	}
	if _, err := loadBody(t, "[telemetry]\nlevel = \"info\"\n"+poolBody, "rou2"); err != nil {
		t.Errorf("a pool at info with its own provider file: %v", err)
	}
}
