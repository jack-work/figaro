package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/rpc"
)

// runSetArgs patches a form key. Supports dotted paths like
// system.tags[42].cache_control.
func runSetArgs(loaded *config.Loaded, ariaID, keyArg, raw string) {

	value := json.RawMessage(raw)
	if !json.Valid(value) {
		s, _ := json.Marshal(raw)
		value = s
	}

	top, path, err := parseFormPath(keyArg)
	if err != nil {
		die("set: %s", err)
	}

	var topValue json.RawMessage
	var ifVersion uint64
	if len(path) == 0 {
		topValue = value
	} else {
		current, version := mustFetchFormKey(loaded, ariaID, top)
		merged, err := deepSetJSON(current, path, value)
		if err != nil {
			die("set: %s", err)
		}
		topValue, ifVersion = merged, version
	}

	patch := rpc.FormPatch{Set: map[string]json.RawMessage{top: topValue}}
	resp := mustCallSet(loaded, ariaID, patch, ifVersion)
	fmt.Fprintf(os.Stderr, "set %s = %s (figaro %s)\n", keyArg, value, resp.figaroID)
}

// runUnsetArgs removes form keys.
func runUnsetArgs(loaded *config.Loaded, ariaID string, args []string) {
	patch := rpc.FormPatch{}
	var ifVersion uint64
	for _, keyArg := range args {
		top, path, err := parseFormPath(keyArg)
		if err != nil {
			die("unset: %s", err)
		}
		if len(path) == 0 {
			patch.Remove = append(patch.Remove, top)
			continue
		}
		current, version := mustFetchFormKey(loaded, ariaID, top)
		if len(current) == 0 {
			continue
		}
		ifVersion = version
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
	resp := mustCallSet(loaded, ariaID, patch, ifVersion)
	fmt.Fprintf(os.Stderr, "unset %s (figaro %s)\n", strings.Join(args, ", "), resp.figaroID)
}

// runForm prints the current form snapshot.
func runForm(loaded *config.Loaded, ariaID string) {
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			die("form: %s", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(nestSnapshot(resp.Snapshot))
	})
}

// nestSnapshot rebuilds the tree the dotted keys describe. Flatness is how the
// form is STORED — one key, one value, one patch record — not what it is: a
// reader wants `system.model` under `system`, and a script wants to walk it.
//
// A key whose prefix is already a leaf keeps its dotted name rather than
// overwriting that leaf: `a` and `a.b` can both be set, and neither may make
// the other unreadable.
func nestSnapshot(snap form.Snapshot) map[string]any {
	root := map[string]any{}
	for k, v := range snap.All() {
		segments := strings.Split(k, ".")
		node := root
		ok := true
		for _, seg := range segments[:len(segments)-1] {
			child, seen := node[seg]
			if !seen {
				child = map[string]any{}
				node[seg] = child
			}
			branch, isBranch := child.(map[string]any)
			if !isBranch {
				ok = false
				break
			}
			node = branch
		}
		if !ok {
			root[k] = json.RawMessage(v)
			continue
		}
		leaf := segments[len(segments)-1]
		if _, taken := node[leaf].(map[string]any); taken {
			root[k] = json.RawMessage(v)
			continue
		}
		node[leaf] = json.RawMessage(v)
	}
	return root
}

// parseFormPath splits a dotted key into top-level key + segments.
func parseFormPath(s string) (string, []string, error) {
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

// deepSetJSON sets a value at a nested path.
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

// deepDeleteJSON deletes a value at a nested path. Returns nil if the
// top-level key should be removed.
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

// fetchFormSnapshot returns the aria's live form snapshot via the
// angelus, or an empty snapshot on failure (best-effort — callers degrade
// gracefully).
func fetchFormSnapshot(loaded *config.Loaded, ariaID string) form.Snapshot {
	var snap form.Snapshot
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if resp, err := s.Figaro.Form(ctx); err == nil {
			snap = resp.Snapshot
		}
		return nil
	})
	return snap
}

// mustFetchFormKey reads one key AND the version the board stood at, so
// the write that follows can be conditional: editing inside a value means
// reading it first, and two shells doing that must not clobber each other.
func mustFetchFormKey(loaded *config.Loaded, ariaID, key string) (json.RawMessage, uint64) {
	var result json.RawMessage
	var version uint64
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			die("form: %s", err)
		}
		result, _ = resp.Snapshot.Get(key)
		version = resp.Version
		return nil
	})
	return result, version
}

type setResult struct {
	figaroID string
	resp     *rpc.SetResponse
}

func mustCallSet(loaded *config.Loaded, ariaID string, patch rpc.FormPatch, ifVersion uint64) setResult {
	var result setResult
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Set(ctx, patch, ifVersion)
		if err != nil {
			die("set: %s", err)
		}
		result = setResult{figaroID: s.AriaID, resp: resp}
		return nil
	})
	return result
}
