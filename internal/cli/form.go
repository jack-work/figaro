package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/cmdkit"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/outfit"
)

// setWait is `--wait`: the writer's verdict instead of the queued
// acknowledgement. Set once by the flag before any entry point runs.
var setWait bool

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

	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{top: topValue}, nil)
	resp := mustCallSet(loaded, ariaID, patch, ifVersion)
	fmt.Fprintf(stderrw, "%s %s = %s (figaro %s)%s\n",
		resp.verb("set"), keyArg, value, resp.figaroID, resp.at())
}

func runFormSet(loaded *config.Loaded, ariaID string, args []string) error {
	switch len(args) {
	case 0:
		return fmt.Errorf("usage: form set <key> <value> | form set k=v,k2=v2")
	case 2:
		if !strings.ContainsAny(args[0], "={") {
			runSetArgs(loaded, ariaID, args[0], args[1])
			return nil
		}
	}
	patch, err := outfit.ParseSet(strings.Join(args, ","))
	if err != nil {
		return err
	}
	if patch.IsIdentity() {
		return fmt.Errorf("form set: %q sets nothing", strings.Join(args, " "))
	}
	resp := mustCallSet(loaded, ariaID, patch, 0)
	fmt.Fprintf(stderrw, "%s %s (figaro %s)%s\n",
		resp.verb("set"), strings.Join(resp.resp.Set, ", "), resp.figaroID, resp.at())
	return nil
}

// runFormDelete takes key paths, comma-separated; `unset` is the same verb.
func runFormDelete(loaded *config.Loaded, ariaID string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: form delete <path>[,<path>…]")
	}
	var paths []string
	for _, a := range args {
		more, err := outfit.ParseDelete(a)
		if err != nil {
			return err
		}
		paths = append(paths, more...)
	}
	if len(paths) == 0 {
		return fmt.Errorf("form delete: no key paths given")
	}
	runUnsetArgs(loaded, ariaID, paths)
	return nil
}

func runFormHelp(ctx *cmdkit.RunContext, args []string) error {
	if len(args) == 0 {
		ctx.Help("state")
		return nil
	}
	if ctx.Help(args[0]) {
		return nil
	}
	return fmt.Errorf("no help topic %q (try `figaro help` for the list)", args[0])
}

func runUnsetArgs(loaded *config.Loaded, ariaID string, args []string) {
	keys := map[string]json.RawMessage{}
	var drops []string
	var ifVersion uint64
	for _, keyArg := range args {
		top, path, err := parseFormPath(keyArg)
		if err != nil {
			die("unset: %s", err)
		}
		if len(path) == 0 {
			drops = append(drops, top)
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
			drops = append(drops, top)
			continue
		}
		keys[top] = pruned
	}
	if len(keys) == 0 && len(drops) == 0 {
		fmt.Fprintln(stderrw, "unset: nothing to do")
		return
	}
	// An absent key is a refusal, not a silent success.
	resp := mustCallSetAsserting(loaded, ariaID, form.Build(form.Snapshot{}, keys, drops), ifVersion)
	fmt.Fprintf(stderrw, "%s %s (figaro %s)%s\n",
		resp.verb("unset"), strings.Join(args, ", "), resp.figaroID, resp.at())
}

func runForm(loaded *config.Loaded, ariaID string) {
	// `<host>/<intrinsic>`: "" is the identity segment, the board.
	spec, intrinsic := splitIntrinsic(ariaID)
	if !knownIntrinsic(intrinsic) {
		die("no intrinsic %q (have: %s; %s is the host's own form, and is implied)",
			intrinsic, strings.Join(intrinsicNames(), ", "), intrinsicState)
	}
	WithSessionFor(loaded, spec, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.FormOf(ctx, intrinsic)
		if err != nil {
			die("form: %s", err)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(nestSnapshot(resp.Snapshot))
	})
}

// Flat is how the form is stored, not what it is.
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

// Returns nil if the top-level key should be removed.
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

// fetchFormSnapshot is best-effort: an empty snapshot on failure.
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

// mustFetchFormKey reads the version too, so the write that follows can be conditional.
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

// verb reports what happened: a set of the value already held reduces to nothing.
func (r setResult) verb(did string) string {
	if r.resp == nil {
		return did
	}
	switch r.resp.Outcome {
	case rpc.OutcomeUnchanged:
		return "unchanged:"
	default:
		return did
	}
}

func (r setResult) at() string {
	if r.resp == nil || r.resp.Version == 0 {
		return ""
	}
	return fmt.Sprintf(" @%d", r.resp.Version)
}

func mustCallSetAsserting(loaded *config.Loaded, ariaID string, patch rpc.FormPatch, ifVersion uint64) setResult {
	return callSet(loaded, ariaID, patch, ifVersion, true)
}

func mustCallSet(loaded *config.Loaded, ariaID string, patch rpc.FormPatch, ifVersion uint64) setResult {
	return callSet(loaded, ariaID, patch, ifVersion, false)
}

func callSet(loaded *config.Loaded, ariaID string, patch rpc.FormPatch, ifVersion uint64, assert bool) setResult {
	var result setResult
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		// An aria mid-turn answers when the round ends, so --wait's
		// timeout is longer.
		timeout := 10 * time.Second
		if setWait {
			timeout = 5 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		set := s.Figaro.Set
		if assert {
			set = s.Figaro.SetAsserting
		}
		if setWait {
			set = func(ctx context.Context, p rpc.FormPatch, v uint64) (*rpc.SetResponse, error) {
				return s.Figaro.SetWaiting(ctx, p, v, assert)
			}
		}
		resp, err := set(ctx, patch, ifVersion)
		if err != nil {
			die("set: %s", err)
		}
		result = setResult{figaroID: s.AriaID, resp: resp}
		return nil
	})
	return result
}
