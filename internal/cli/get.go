package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

func runGet(loaded *config.Loaded, ariaID string, paths []string, asJSON bool) error {
	for _, p := range paths {
		if _, _, err := parseFormPath(p); err != nil {
			return fmt.Errorf("get: %s", err)
		}
	}
	var snap form.Snapshot
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			return fmt.Errorf("form: %w", err)
		}
		snap = resp.Snapshot
		return nil
	})
	return writeGot(stdout, snap, paths, asJSON)
}

func writeGot(w io.Writer, snap form.Snapshot, paths []string, asJSON bool) error {
	var missing []error
	found := map[string]json.RawMessage{}
	var order []string
	for _, p := range paths {
		v, ok := formValueAt(snap, p)
		if !ok {
			missing = append(missing, fmt.Errorf("no key %s", p))
			continue
		}
		if _, dup := found[p]; !dup {
			order = append(order, p)
		}
		found[p] = v
	}
	if asJSON && len(paths) > 1 {
		var b bytes.Buffer
		b.WriteString("{")
		for i, p := range order {
			if i > 0 {
				b.WriteString(",")
			}
			k, _ := json.Marshal(p)
			b.Write(k)
			b.WriteString(":")
			b.Write(found[p])
		}
		b.WriteString("}")
		if err := writeIndented(w, b.Bytes()); err != nil {
			return err
		}
		return errors.Join(missing...)
	}
	for _, p := range paths {
		v, ok := found[p]
		if !ok {
			continue
		}
		if err := writeValue(w, v, asJSON); err != nil {
			return err
		}
	}
	return errors.Join(missing...)
}

func formValueAt(snap form.Snapshot, p string) (json.RawMessage, bool) {
	top, path, err := parseFormPath(p)
	if err != nil {
		return nil, false
	}
	v, ok := snap.Get(top)
	if !ok {
		return nil, false
	}
	return jsonAt(v, path)
}

func jsonAt(v json.RawMessage, path []string) (json.RawMessage, bool) {
	for _, seg := range path {
		var obj map[string]json.RawMessage
		if json.Unmarshal(v, &obj) == nil && obj != nil {
			next, ok := obj[seg]
			if !ok {
				return nil, false
			}
			v = next
			continue
		}
		var arr []json.RawMessage
		if json.Unmarshal(v, &arr) != nil {
			return nil, false
		}
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(arr) {
			return nil, false
		}
		v = arr[i]
	}
	return v, true
}

func writeValue(w io.Writer, v json.RawMessage, asJSON bool) error {
	trimmed := bytes.TrimSpace(v)
	if !asJSON && len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if json.Unmarshal(trimmed, &s) == nil {
			_, err := fmt.Fprintln(w, s)
			return err
		}
	}
	return writeIndented(w, trimmed)
}

func writeIndented(w io.Writer, v []byte) error {
	var b bytes.Buffer
	if err := json.Indent(&b, v, "", "  "); err != nil {
		b.Reset()
		b.Write(v)
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

const cwdKey = "system.cwd"

func runPwd(loaded *config.Loaded, ariaID string, asJSON bool) error {
	var dir string
	WithSessionFor(loaded, ariaID, func(s *Session) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := s.Figaro.Form(ctx)
		if err != nil {
			return fmt.Errorf("form: %w", err)
		}
		if c := resp.Snapshot.Lookup(cwdKey); c != nil && *c != "" {
			dir = *c
			return nil
		}
		if node := foreignNode(s.AriaID); node != "" {
			return fmt.Errorf("pwd: %s is unset, and the daemon's own directory is on node %s", cwdKey, node)
		}
		d, err := daemonCwd(ctx, s.Angelus.Status)
		if err != nil {
			return fmt.Errorf("pwd: %s is unset, and %w", cwdKey, err)
		}
		dir = d
		return nil
	})
	if asJSON {
		b, _ := json.Marshal(dir)
		_, err := fmt.Fprintln(stdout, string(b))
		return err
	}
	_, err := fmt.Fprintln(stdout, dir)
	return err
}

func daemonCwd(ctx context.Context, status func(context.Context) (*rpc.StatusResponse, error)) (string, error) {
	st, err := status(ctx)
	if err != nil {
		return "", err
	}
	if st.Cwd == "" {
		return "", fmt.Errorf("the daemon does not report its own directory")
	}
	return st.Cwd, nil
}

var dotGetVerbs = map[string]bool{"show": true, "history": true}

var dotGetShowValueFlags = map[string]bool{
	"--id": true, "--from": true, "--to": true, "--before": true, "--max-bytes": true, "--last": true, "-n": true,
}

func dotGetArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return args, nil
	}
	if isDotPath(args[0]) {
		return dotGetRest(args)
	}
	if !dotGetVerbs[args[0]] {
		return args, nil
	}
	rest := args[1:]
	dotted := false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			break
		}
		if dotGetShowValueFlags[a] {
			i++
			continue
		}
		if isDotPath(a) {
			dotted = true
			break
		}
	}
	if !dotted {
		return args, nil
	}
	var kept []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--id":
			if i+1 < len(rest) {
				kept = append(kept, a, rest[i+1])
				i++
			}
		case strings.HasPrefix(a, "--id="), a == "-j", a == "--json":
			kept = append(kept, a)
		case strings.HasPrefix(a, "-"):
			return nil, fmt.Errorf("%s .<path> takes only --id and -j (%s is a show flag); or use figaro get", args[0], a)
		default:
			kept = append(kept, a)
		}
	}
	return dotGetRest(kept)
}

func dotGetRest(rest []string) ([]string, error) {
	out := []string{"get"}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--id" && i+1 < len(rest):
			out = append(out, a, rest[i+1])
			i++
		case strings.HasPrefix(a, "."):
			if isLTRef(a) {
				return nil, fmt.Errorf("%s is an LT coordinate, not a form path; for a key named %s use figaro get %s", a, a[1:], a[1:])
			}
			if len(a) == 1 {
				return nil, fmt.Errorf(". names no form path")
			}
			out = append(out, a[1:])
		case strings.HasPrefix(a, "-"):
			out = append(out, a)
		default:
			return nil, fmt.Errorf("%s is not a .<path>: give every path its leading dot, or use figaro get", a)
		}
	}
	return out, nil
}

func isDotPath(a string) bool {
	return len(a) > 1 && a[0] == '.' && a[1] != '.' && a[1] != '/'
}

func isLTRef(a string) bool {
	if len(a) < 2 || a[0] != '.' {
		return false
	}
	_, err := strconv.ParseUint(a[1:], 10, 64)
	return err == nil
}
