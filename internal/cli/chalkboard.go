package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/transport"
)

// runSet patches a chalkboard key on the figaro bound to this shell.
// Usage: figaro set <key> <value>
//
// The key may be a simple top-level chalkboard name, or a path with
// `.field` and `[index]` segments — e.g. `system.tags[42].cache_control`.
// In path mode, the existing top-level value is fetched, the leaf is
// merged in, and the resulting top-level value is sent back as a Set.
func runSet(loaded *config.Loaded) {
	if len(os.Args) < 4 {
		die("usage: figaro set <key> <value>")
	}
	keyArg := os.Args[2]
	raw := os.Args[3]

	value := json.RawMessage(raw)
	if !json.Valid(value) {
		s, _ := json.Marshal(raw)
		value = s
	}

	top, path, err := parseChalkboardPath(keyArg)
	if err != nil {
		die("set: %s", err)
	}

	var topValue json.RawMessage
	if len(path) == 0 {
		topValue = value
	} else {
		current := mustFetchChalkboardKey(loaded, top)
		merged, err := deepSetJSON(current, path, value)
		if err != nil {
			die("set: %s", err)
		}
		topValue = merged
	}

	patch := rpc.ChalkboardPatch{Set: map[string]json.RawMessage{top: topValue}}
	resp := mustCallSet(loaded, patch)
	fmt.Fprintf(os.Stderr, "set %s = %s (figaro %s)\n", keyArg, value, resp.figaroID)
}

// runUnset removes one or more chalkboard keys.
// Usage: figaro unset <key> [<key>...]
func runUnset(loaded *config.Loaded) {
	if len(os.Args) < 3 {
		die("usage: figaro unset <key> [<key>...]")
	}
	args := os.Args[2:]
	patch := rpc.ChalkboardPatch{}
	for _, keyArg := range args {
		top, path, err := parseChalkboardPath(keyArg)
		if err != nil {
			die("unset: %s", err)
		}
		if len(path) == 0 {
			patch.Remove = append(patch.Remove, top)
			continue
		}
		current := mustFetchChalkboardKey(loaded, top)
		if len(current) == 0 {
			continue
		}
		pruned, dropTop, err := deepDeleteJSON(current, path)
		if err != nil {
			die("unset: %s", err)
		}
		if dropTop {
			patch.Remove = append(patch.Remove, top)
			continue
		}
		if patch.Set == nil {
			patch.Set = map[string]json.RawMessage{}
		}
		patch.Set[top] = pruned
	}
	if len(patch.Set) == 0 && len(patch.Remove) == 0 {
		fmt.Fprintln(os.Stderr, "unset: nothing to do")
		return
	}
	resp := mustCallSet(loaded, patch)
	fmt.Fprintf(os.Stderr, "unset %s (figaro %s)\n", strings.Join(args, ", "), resp.figaroID)
}

// runChalkboard prints the current chalkboard snapshot of the
// figaro bound to this shell. Keys are sorted alphabetically.
func runChalkboard(loaded *config.Loaded) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil {
		die("resolve: %s", err)
	}
	if !r.Found {
		die("no figaro bound to this shell")
	}
	ep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := figaro.DialClient(ep, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Chalkboard(ctx)
	if err != nil {
		die("chalkboard: %s", err)
	}
	printSnapshot(os.Stdout, resp.Snapshot)
}

func printSnapshot(w io.Writer, snap map[string]json.RawMessage) {
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s = %s\n", k, snap[k])
	}
}

// parseChalkboardPath splits a key like `system.tags[42].cache_control`
// into a top-level chalkboard key (`system.tags`) and a path of leaf
// segments (`["42", "cache_control"]`).
func parseChalkboardPath(s string) (string, []string, error) {
	if s == "" {
		return "", nil, fmt.Errorf("empty key")
	}
	bracket := strings.IndexByte(s, '[')
	if bracket < 0 {
		return s, nil, nil
	}
	top := s[:bracket]
	if top == "" {
		return "", nil, fmt.Errorf("key cannot start with '['")
	}
	rest := s[bracket:]
	var path []string
	for len(rest) > 0 {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := len(rest)
			for i := 0; i < len(rest); i++ {
				if rest[i] == '.' || rest[i] == '[' {
					end = i
					break
				}
			}
			if end == 0 {
				return "", nil, fmt.Errorf("empty segment after '.'")
			}
			path = append(path, rest[:end])
			rest = rest[end:]
		case '[':
			closeIdx := strings.IndexByte(rest, ']')
			if closeIdx < 0 {
				return "", nil, fmt.Errorf("unclosed '[' in key")
			}
			inner := rest[1:closeIdx]
			if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
				var tok string
				if err := json.Unmarshal([]byte(inner), &tok); err != nil {
					return "", nil, fmt.Errorf("bracket token: %w", err)
				}
				path = append(path, tok)
			} else {
				path = append(path, inner)
			}
			rest = rest[closeIdx+1:]
		default:
			return "", nil, fmt.Errorf("unexpected %q in key", rest[0])
		}
	}
	return top, path, nil
}

// deepSetJSON walks/creates an object path inside `current` and sets
// the leaf to `value`.
func deepSetJSON(current json.RawMessage, path []string, value json.RawMessage) (json.RawMessage, error) {
	var root any
	if len(current) == 0 || string(current) == "null" {
		root = map[string]any{}
	} else if err := json.Unmarshal(current, &root); err != nil {
		root = map[string]any{}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		obj = map[string]any{}
	}
	cursor := obj
	for i, seg := range path {
		if i == len(path)-1 {
			var v any
			if err := json.Unmarshal(value, &v); err != nil {
				return nil, fmt.Errorf("value: %w", err)
			}
			cursor[seg] = v
			break
		}
		next, ok := cursor[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[seg] = next
		}
		cursor = next
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// deepDeleteJSON walks an object path inside `current` and deletes the
// leaf segment. Returns the resulting top-level JSON, or `nil` (signal
// for the caller to remove the top-level chalkboard key entirely) if
// pruning empty parents bubbles all the way to the root.
func deepDeleteJSON(current json.RawMessage, path []string) (json.RawMessage, bool, error) {
	if len(path) == 0 {
		return nil, true, nil
	}
	if len(current) == 0 || string(current) == "null" {
		return current, false, nil
	}
	var root any
	if err := json.Unmarshal(current, &root); err != nil {
		return current, false, nil
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return current, false, nil
	}
	if !deepDeleteWalk(obj, path) {
		return current, false, nil
	}
	if len(obj) == 0 {
		return nil, true, nil
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func deepDeleteWalk(obj map[string]any, path []string) bool {
	if len(path) == 1 {
		if _, ok := obj[path[0]]; !ok {
			return false
		}
		delete(obj, path[0])
		return true
	}
	next, ok := obj[path[0]].(map[string]any)
	if !ok {
		return false
	}
	changed := deepDeleteWalk(next, path[1:])
	if changed && len(next) == 0 {
		delete(obj, path[0])
	}
	return changed
}

func mustFetchChalkboardKey(loaded *config.Loaded, key string) json.RawMessage {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acli := mustConnectAngelus(loaded)
	defer acli.Close()
	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil {
		die("resolve: %s", err)
	}
	if !r.Found {
		die("no figaro bound to this shell")
	}
	ep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := figaro.DialClient(ep, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()
	resp, err := fcli.Chalkboard(ctx)
	if err != nil {
		die("chalkboard: %s", err)
	}
	return resp.Snapshot[key]
}

type setResult struct {
	figaroID string
	resp     *rpc.SetResponse
}

func mustCallSet(loaded *config.Loaded, patch rpc.ChalkboardPatch) setResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	acli := mustConnectAngelus(loaded)
	defer acli.Close()

	r, err := acli.Resolve(ctx, os.Getppid())
	if err != nil {
		die("resolve: %s", err)
	}
	if !r.Found {
		die("no figaro bound to this shell")
	}
	ep := transport.Endpoint{Scheme: r.Endpoint.Scheme, Address: r.Endpoint.Address}
	fcli, err := figaro.DialClient(ep, nil)
	if err != nil {
		die("connect figaro: %s", err)
	}
	defer fcli.Close()

	resp, err := fcli.Set(ctx, patch)
	if err != nil {
		die("set: %s", err)
	}
	return setResult{figaroID: r.FigaroID, resp: resp}
}
