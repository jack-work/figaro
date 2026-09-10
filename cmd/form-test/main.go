package main

// form-test: a wire microscope for ONE form. It exists to be watched, so it
// prints the exact bytes and nothing it cannot show you the source of.
//
//	form-test listen <@form>        dial the form's socket, dump every frame
//	form-test send   <@form> k=v…   figaro.set, dump request and response
//
// It speaks the wire directly (no sdk) so that what you read here is what
// goes over the socket.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"path/filepath"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: form-test <listen|send> <@form> [k=v …]")
		os.Exit(2)
	}
	verb, id := os.Args[1], os.Args[2]

	sock, err := formSocket(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}
	switch verb {
	case "listen":
		listen(id, sock)
	case "send":
		send(id, sock, os.Args[3:])
	default:
		fmt.Fprintln(os.Stderr, "unknown verb", verb)
		os.Exit(2)
	}
}

// formSocket asks the angelus where the form's endpoint is. This is the ONE
// exchange that cannot happen on the form's own socket, for the obvious
// reason: you do not know where that socket is yet.
func formSocket(id string) (string, error) {
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(
		filepath.Join(runtimeDir(), "angelus.sock")))
	if err != nil {
		return "", err
	}
	defer acli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := acli.Attach(ctx, id)
	if err != nil {
		return "", err
	}
	fmt.Printf("%s  angelus.attach → %s\n", dim("resolve"), resp.Endpoint.Address)
	return resp.Endpoint.Address, nil
}

func runtimeDir() string {
	if d := os.Getenv("FIGARO_RUNTIME_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "figaro")
	}
	return filepath.Join(os.TempDir(), "figaro")
}

// ---------------------------------------------------------------------------
// listen
// ---------------------------------------------------------------------------

func listen(id, sock string) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Printf("%s  %s\n", dim("socket"), sock)
	fmt.Printf("%s  no subscribe call: an open connection IS the subscription\n\n", dim("note"))

	// Seed: figaro.form, so the mirror starts from a snapshot and the deltas
	// that follow have something to apply to.
	mirror := map[string]json.RawMessage{}
	version := uint64(0)

	enc := json.NewEncoder(conn)
	rd := bufio.NewReader(conn)

	seed := map[string]any{"jsonrpc": "2.0", "id": 1, "method": rpc.MethodForm}
	dump("→", seed)
	_ = enc.Encode(seed)

	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			fmt.Println(dim("connection closed:"), err)
			return
		}
		var frame map[string]json.RawMessage
		if json.Unmarshal(line, &frame) != nil {
			continue
		}
		var raw any
		_ = json.Unmarshal(line, &raw)
		dump("←", raw)

		// A response to our seed.
		if res, ok := frame["result"]; ok {
			var fr rpc.FormResponse
			if json.Unmarshal(res, &fr) == nil {
				for k, v := range fr.Snapshot.All() {
					mirror[k] = v
				}
				version = fr.Version
				render(id, mirror, version)
			}
			continue
		}
		// A pushed notification.
		var method string
		if m, ok := frame["method"]; ok {
			_ = json.Unmarshal(m, &method)
		}
		if method != rpc.MethodFormDelta {
			continue
		}
		var d rpc.FormDelta
		if json.Unmarshal(frame["params"], &d) != nil {
			continue
		}
		applyDelta(mirror, d)
		version = d.Version
		render(id, mirror, version)
	}
}

// applyDelta folds one patch into the flat mirror. The real client uses
// form.Snapshot.Apply; this walks the object patch by hand so you can SEE
// what set and delete mean on the wire.
func applyDelta(mirror map[string]json.RawMessage, d rpc.FormDelta) {
	if d.Patch.Object == nil {
		return
	}
	for k, v := range d.Patch.Object.Set {
		b, _ := json.Marshal(v)
		mirror[k] = b
	}
	for k := range d.Patch.Object.Delete {
		delete(mirror, k)
	}
	for k, sub := range d.Patch.Object.Update {
		if sub.Scalar != nil {
			b, _ := json.Marshal(sub.Scalar.After)
			mirror[k] = b
		}
	}
}

// render draws the derived view: the whole point of holding a mirror.
func render(id string, mirror map[string]json.RawMessage, version uint64) {
	keys := make([]string, 0, len(mirror))
	for k := range mirror {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("\n%s %s %s\n", bold("┌─ "+id), dim("v"+fmt.Sprint(version)),
		dim(strings.Repeat("─", max(0, 40-len(id)))))
	for _, k := range keys {
		v := string(mirror[k])
		if len(v) > 60 {
			v = v[:60] + "…"
		}
		fmt.Printf("%s %-28s %s\n", bold("│"), k, v)
	}
	fmt.Printf("%s\n\n", bold("└"+strings.Repeat("─", 44)))
}

// ---------------------------------------------------------------------------
// send
// ---------------------------------------------------------------------------

func send(id, sock string, kvs []string) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close()

	set := map[string]any{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintln(os.Stderr, "expected k=v, got", kv)
			os.Exit(2)
		}
		// A bare word is a JSON string; anything that parses is itself.
		var probe any
		if json.Unmarshal([]byte(v), &probe) == nil {
			set[k] = json.RawMessage(v)
		} else {
			set[k] = v
		}
	}

	req := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": rpc.MethodSet,
		"params": map[string]any{"patch": map[string]any{"object": map[string]any{"Set": set}}},
	}
	dump("→", req)
	enc := json.NewEncoder(conn)
	if err := enc.Encode(req); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	rd := bufio.NewReader(conn)
	// NOTIFICATIONS INTERLEAVE WITH RESPONSES on the same connection, and the
	// push usually WINS: this connection is an attached subscriber the moment
	// it opens, so `form.delta` for our own write arrives before the reply to
	// it. A client that reads one line and calls it the answer reads the
	// wrong frame. Match on `id`.
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		var raw any
		_ = json.Unmarshal(line, &raw)
		dump("←", raw)
		var frame struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &frame) == nil && len(frame.ID) > 0 {
			return // our reply, matched by id
		}
	}
}

// ---------------------------------------------------------------------------

func dump(dir string, v any) {
	b, _ := json.MarshalIndent(v, "  ", "  ")
	arrow := dir
	if dir == "→" {
		arrow = "\x1b[36m" + dir + "\x1b[0m"
	} else {
		arrow = "\x1b[35m" + dir + "\x1b[0m"
	}
	fmt.Printf("%s %s %s\n", arrow, dim(time.Now().Format("15:04:05.000")), string(b))
}

func dim(s string) string  { return "\x1b[2m" + s + "\x1b[0m" }
func bold(s string) string { return "\x1b[1m" + s + "\x1b[0m" }
