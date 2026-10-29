package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
)

func main() {
	ctx := context.Background()
	sock := os.Getenv("FIGARO_RUNTIME_DIR") + "/angelus.sock"
	ep := transport.UnixEndpoint(sock)
	acli, err := sdk.DialAngelus(ep)
	must(err)
	switch os.Args[1] {
	case "create":
		var keys map[string]json.RawMessage
		b, _ := os.ReadFile(os.Args[2])
		must(json.Unmarshal(b, &keys))
		p := form.Build(form.Snapshot{}, keys, nil)
		t0 := time.Now()
		resp, err := acli.FormCreate(ctx, "", nil, &p)
		must(err)
		fmt.Println(resp.FormID, time.Since(t0))
	case "history":
		id, n := os.Args[2], atoi(os.Args[3])
		a, err := sdk.DialAria(sdk.Addr{Door: ep, ID: id}, nil)
		must(err)
		t0 := time.Now()
		for i := 0; i < n; i++ {
			raw, _ := json.Marshal(fmt.Sprintf("value %d", i))
			key := fmt.Sprintf("h.k%04d", i%500)
			_, err := a.Set(ctx, form.Build(form.Snapshot{}, map[string]json.RawMessage{key: raw}, nil), 0)
			must(err)
		}
		fmt.Println(id, n, "patches", time.Since(t0))
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
func must(err error) {
	if err != nil {
		panic(err)
	}
}
