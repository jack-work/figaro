package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const poolBody = `[authz]
policy = "policy.toml"

[worker.pools.chat-groups]
enabled = true
outfit = "broker-group"
provider = "rou2"
rou2_lease_class = "chat-group"
rou2_url = "http://127.0.0.1:9110"
say_url = "https://broker.example/groups/say"
workers = ["worker0", "worker1"]
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
	if got := p.WorkerNames(); len(got) != 2 || got[0] != "worker0" || got[1] != "worker1" {
		t.Fatalf("workers %v", got)
	}
	if p.Rou2URL != "http://127.0.0.1:9110" {
		t.Fatalf("rou2_url %q", p.Rou2URL)
	}
	if m, d := p.SearchBounds(); p.SuccessionBound() != 4 || p.DeliverCap() != 16<<10 || p.SayBound() != 20 || m != 6 || d != 100 || p.SearchURL != "" {
		t.Fatalf("defaults: %d successions a day, %d bytes a deliver, %d says a minute", p.SuccessionBound(), p.DeliverCap(), p.SayBound())
	}
	if _, ok := l.Pool("other"); ok {
		t.Fatal("a pool nobody declared was found")
	}
}

// A config with no pool is every daemon there was before pools: it must load
// as it did, and it is not a pool daemon.
func TestAConfigWithoutAPoolIsNotAPoolDaemons(t *testing.T) {
	l, err := loadBody(t, "[sandbox.boxes.worker3-g7]\nexec = \"/run/box/a.sock\"\n[telemetry]\nlevel = \"debug\"\n", "anthropic")
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
	workers := `workers = ["worker0", "worker1"]` + "\n"
	for name, c := range map[string]struct{ body, want string }{
		"a worker with an exec path":  {swap(workers, `workers = ["worker0", {name = "worker1", exec = "/run/box/worker1.sock"}]`+"\n"), "carrying exec"},
		"a worker with a door":        {swap(workers, `workers = [{name = "worker0", door = "/run/door/worker0.sock"}]`+"\n"), "carrying door"},
		"a worker with a reset":       {swap(workers, `workers = [{name = "worker0", reset = "/run/reset/worker0"}]`+"\n"), "carrying name, reset"},
		"workers as tables":           {swap(workers, "[worker.pools.chat-groups.workers.worker0]\nexec = \"/run/box/worker0.sock\"\n"), "incompatible types"},
		"a worker that is a number":   {swap(workers, "workers = [3]\n"), "a worker is a name"},
		"no workers":                  {swap(workers, ""), "can lease nothing"},
		"an exec path on the pool":    {poolBody + "exec = \"/run/box/pool.sock\"\n", "worker.pools.chat-groups.exec"},
		"a misspelt enabled":          {swap("enabled = true", "enabeld = false"), "worker.pools.chat-groups.enabeld"},
		"an unknown key under worker": {poolBody + "[worker.reset]\ncommand = \"/bin/reset\"\n", "worker.reset"},
		"no outfit":                   {swap("outfit = \"broker-group\"\n", ""), "outfit"},
		"the outfit named default":    {swap(`"broker-group"`, `"default"`), "daemon's own figaros"},
		"the config's default outfit": {"default_outfit = \"house\"\n" + swap(`"broker-group"`, `"house"`), "daemon's own figaros"},
		"no provider":                 {swap("provider = \"rou2\"\n", ""), "provider"},
		"no lease class":              {swap("rou2_lease_class = \"chat-group\"\n", ""), "rou2_lease_class"},
		"no rou2 url":                 {swap("rou2_url = \"http://127.0.0.1:9110\"\n", ""), "rou2_url"},
		"a rou2 url with a path":      {swap("http://127.0.0.1:9110", "http://127.0.0.1:9110/u/explabs/v1"), "rou2_url"},
		"a rou2 url with a user":      {swap("http://127.0.0.1:9110", "http://u:p@127.0.0.1:9110"), "rou2_url"},
		"a rou2 url on a file":        {swap("http://127.0.0.1:9110", "file:///run/rou2"), "rou2_url"},
		"a route of broker's":         {poolBody + "page_route = \"admin\"\n", "worker.pools.chat-groups.page_route: not a key figaro knows"},
		"a figaro to page":            {poolBody + "page = \"@owner\"\n", "worker.pools.chat-groups.page: not a key figaro knows"},
		"no say url":                  {swap("say_url = \"https://broker.example/groups/say\"\n", ""), "say_url"},
		"a say url with no scheme":    {swap("https://broker.example/groups/say", "broker.example/groups/say"), "say_url"},
		"a say url with a password":   {swap("https://broker.example", "https://u:p@broker.example"), "say_url"},
		"a say url on a file":         {swap("https://broker.example/groups/say", "file:///etc/passwd"), "say_url"},
		"a say url in clear":          {swap("https://broker.example/groups/say", "http://broker.example/groups/say"), "say_url"},
		"a say url in clear by ip":    {swap("https://broker.example/groups/say", "http://10.0.0.7/groups/say"), "say_url"},
		"a say url in clear, 0.0.0.0": {swap("https://broker.example/groups/say", "http://0.0.0.0:4380/say"), "say_url"},
		"a say url, localhost.evil":   {swap("https://broker.example/groups/say", "http://localhost.example/say"), "say_url"},
		"a say url, ws on loopback":   {swap("https://broker.example/groups/say", "ws://127.0.0.1:4380/say"), "say_url"},
		"a say socket not absolute":   {swap("https://broker.example/groups/say", "unix:run/pool.sock"), "say_url"},
		"a say socket, no path":       {swap("https://broker.example/groups/say", "unix:"), "say_url"},
		"a say socket not clean":      {swap("https://broker.example/groups/say", "unix:/run/../pool.sock"), "say_url"},
		"a say socket with a host":    {swap("https://broker.example/groups/say", "unix://host/pool.sock"), "say_url"},
		"a say socket, relative path": {swap("https://broker.example/groups/say", "unix:/run/pool.sock:say"), "say_url"},
		"a say socket, url for path":  {swap("https://broker.example/groups/say", "unix:/run/pool.sock:http://broker.example/say"), "say_url"},
		"a say socket, //host path":   {swap("https://broker.example/groups/say", "unix:/run/pool.sock://broker.example/say"), "say_url"},
		"a search url on a socket":    {poolBody + "search_url = \"unix:/run/search.sock\"\n", "search_url"},
		"a search url, ftp on lo":     {poolBody + "search_url = \"ftp://localhost/search\"\n", "search_url"},
		"a search url in clear":       {poolBody + "search_url = \"http://search.example/search\"\n", "search_url"},
		"a search url, 128.0.0.1":     {poolBody + "search_url = \"http://128.0.0.1/search\"\n", "search_url"},
		"a search url, ::2":           {poolBody + "search_url = \"http://[::2]:9/search\"\n", "search_url"},
		"a worker named like a lease": {swap(`"worker1"`, `"worker1-g7"`), "ends like a lease"},
		"a worker with a colon":       {swap(`"worker1"`, `"worker:1"`), "worker name"},
		"an empty worker name":        {swap(`"worker1"`, `""`), "worker name"},
		"a worker named self":         {swap(`"worker1"`, `"self"`), "reserved"},
		"one worker twice":            {swap(`"worker1"`, `"worker0"`), "also a worker of"},
		"one worker in two pools":     {poolBody + strings.Replace(poolBody[strings.Index(poolBody, "[worker.pools"):], "chat-groups", "other", 1), "also a worker of"},
		"a pool name with a slash":    {swap("[worker.pools.chat-groups]", `[worker.pools."chat/groups"]`), "pool name"},
		"negative successions":        {poolBody + "successions_per_day = -1\n", "successions_per_day"},
		"a negative deliver cap":      {poolBody + "deliver_max_bytes = -1\n", "deliver_max_bytes"},
		"a negative say bound":        {poolBody + "say_per_minute = -1\n", "say_per_minute"},
		"a search credential by path": {poolBody + "search_credential = \"/etc/search.key\"\n", "search_credential"},
		"a search credential of dots": {poolBody + "search_credential = \"..\"\n", "search_credential"},
		"a search credential alone":   {poolBody + "search_credential = \"search-groups\"\n", "no search_url"},
		"a search url on a file":      {poolBody + "search_url = \"file:///etc/passwd\"\n", "search_url"},
		"a search url with a user":    {poolBody + "search_url = \"http://u:p@127.0.0.1:9/\"\n", "search_url"},
		"negative searches a minute":  {poolBody + "search_url = \"http://127.0.0.1:9/\"\nsearch_per_minute = -1\n", "search_per_minute"},
		"negative searches a day":     {poolBody + "search_url = \"http://127.0.0.1:9/\"\nsearch_per_day = -1\n", "search_per_day"},
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
	l, err := loadBody(t, poolBody+"successions_per_day = 0\ndeliver_max_bytes = 4096\nsay_per_minute = 3\nsearch_url = \"http://127.0.0.1:4390/search\"\nsearch_credential = \"search-groups\"\nsearch_per_minute = 2\nsearch_per_day = 0\n", "rou2")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := l.Pool("chat-groups")
	if p.SuccessionBound() != 0 || p.DeliverCap() != 4096 || p.SayBound() != 3 || p.SearchURL != "http://127.0.0.1:4390/search" || p.SearchCredential != "search-groups" {
		t.Fatalf("pool %+v", p)
	}
	if m, d := p.SearchBounds(); m != 2 || d != 0 {
		t.Fatalf("search bounds %d a minute, %d a day", m, d)
	}
}

// A bearer goes with every say and search, so a URL in clear is taken
// only for a loopback host.
func TestAPoolSendsItsBearersInClearOnlyOnLoopback(t *testing.T) {
	for _, u := range []string{"https://broker.example/say", "http://127.0.0.1:4380/say", "http://127.8.9.10/say", "http://[::1]:4380/say", "http://localhost:4380/say", "http://LOCALHOST/say"} {
		body := strings.Replace(poolBody, "https://broker.example/groups/say", u, 1) + "search_url = \"" + u + "\"\n"
		if _, err := loadBody(t, body, "rou2"); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

// A say may go to a unix socket on the host instead: no network carries
// its bearer, so it is taken like loopback. The request path follows the
// socket after a colon, and is / when none is named.
func TestAPoolSaysOverAUnixSocketOnTheHost(t *testing.T) {
	for raw, want := range map[string][2]string{
		"unix:/run/gluck-herald/pool.sock":                {"/run/gluck-herald/pool.sock", "/"},
		"unix:/run/gluck-herald/pool.sock:/v1/groups/say": {"/run/gluck-herald/pool.sock", "/v1/groups/say"},
	} {
		l, err := loadBody(t, strings.Replace(poolBody, "https://broker.example/groups/say", raw, 1), "rou2")
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		p, _ := l.Pool("chat-groups")
		socket, path, ok := SayOverUnix(p.SayURL)
		if !ok || socket != want[0] || path != want[1] {
			t.Errorf("%s reads as socket %q path %q (%v), want %q %q", raw, socket, path, ok, want[0], want[1])
		}
	}
	if _, _, ok := SayOverUnix("https://broker.example/say"); ok {
		t.Error("an https say url read as a socket")
	}
}

// A pool daemon takes the outbound token its pool's provider names, and
// no other: a second entry, or one when the provider names none, does not
// load.
func TestAPoolDaemonHoldsOnlyTheServiceTokenOfItsPoolsProvider(t *testing.T) {
	const token = "[service_tokens.rou2]\nissuer = \"https://auth.example\"\nclient_id = \"figaro-pool\"\nsecret_credential = \"rou2-pool\"\n"
	load := func(body, provider string) error {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "providers"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "providers", "rou2.toml"), []byte(provider), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		return err
	}
	if err := load(poolBody+token, "service_token = \"rou2\"\n"); err != nil {
		t.Errorf("the token the pool's provider names: %v", err)
	}
	for name, c := range map[string]struct{ body, provider, want string }{
		"a token the provider does not name": {poolBody + token, "service_token = \"other\"\n", "[service_tokens.rou2]"},
		"a token and a provider with none":   {poolBody + token, "", "[service_tokens.rou2]"},
		"a second token":                     {poolBody + token + "[service_tokens.edge]\nissuer = \"https://auth.example\"\n", "service_token = \"rou2\"\n", "[service_tokens.edge]"},
	} {
		if err := load(c.body, c.provider); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal naming %s", name, err, c.want)
		}
	}
}

// Each thing alone, beside an otherwise good pool. The reason is checked so a
// snippet that fails some other validation cannot pass for the fence.
func TestAPoolDaemonRefusesWhatItCannotHold(t *testing.T) {
	const beside = "beside [worker.pools]"
	for name, c := range map[string]struct {
		body      string
		providers []string
		want      string
	}{
		"a troupe":          {poolBody + "[troupe]\nname = \"kelliher\"\n", nil, "[troupe] " + beside},
		"a peer":            {poolBody + "[peers.gluck]\ndoor = \"gluck:7777\"\n", nil, "[peers] " + beside},
		"a node door":       {poolBody + "[door.tls]\nlisten = \":7777\"\n", nil, "[door.tls] " + beside},
		"a static box":      {poolBody + "[sandbox.boxes.judge]\nexec = \"/run/box/judge.sock\"\n", nil, "[sandbox.boxes] " + beside},
		"a service token":   {poolBody + "[service_tokens.edge]\nissuer = \"https://auth.example\"\n", nil, "[service_tokens.edge] " + beside},
		"an api key":        {strings.Replace(poolBody, "policy = \"policy.toml\"\n", "policy = \"policy.toml\"\n[[authz.api_keys]]\nid = \"k1\"\n", 1), nil, "[authz.api_keys] " + beside},
		"a uid map":         {strings.Replace(poolBody, "policy = \"policy.toml\"\n", "policy = \"policy.toml\"\n[authz.peercred.uids]\n1001 = \"user:guest\"\n", 1), nil, "[authz.peercred] " + beside},
		"no trunks":         {"trunks = false\n" + poolBody, nil, "trunks = false " + beside},
		"a debug level":     {"[telemetry]\nlevel = \"Debug\"\n" + poolBody, nil, "level"},
		"no policy named":   {poolBody[strings.Index(poolBody, "[worker.pools"):], nil, "never falls back to the default policy"},
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
