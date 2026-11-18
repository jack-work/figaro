package form

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Shape struct {
	Name  string
	check func(json.RawMessage) error
}

var errShapeless = errors.New("the registry row has no shape")

func (s Shape) Check(raw json.RawMessage) error {
	if s.check == nil {
		return errShapeless
	}
	return s.check(raw)
}

func (s Shape) Defined() bool { return s.check != nil }

func (s Shape) String() string { return s.Name }

func want(what string, raw json.RawMessage) error {
	return fmt.Errorf("want %s, got %s %s", what, jsonKind(raw), clip(raw))
}

func clip(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

func asString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

func ShapeAny() Shape { return Shape{Name: "any", check: func(json.RawMessage) error { return nil }} }

func ShapeBool() Shape {
	return Shape{Name: "bool", check: func(raw json.RawMessage) error {
		var b bool
		if json.Unmarshal(raw, &b) != nil || isNull(raw) {
			return want("true or false", raw)
		}
		return nil
	}}
}

func ShapeString() Shape {
	return Shape{Name: "string", check: func(raw json.RawMessage) error {
		if _, ok := asString(raw); !ok {
			return want("a string", raw)
		}
		return nil
	}}
}

func ShapeObject() Shape {
	return Shape{Name: "object", check: func(raw json.RawMessage) error {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil || m == nil {
			return want("an object", raw)
		}
		return nil
	}}
}

func ShapeStringOrObject() Shape {
	obj := ShapeObject()
	return Shape{Name: "string or object", check: func(raw json.RawMessage) error {
		if _, ok := asString(raw); ok || obj.check(raw) == nil {
			return nil
		}
		return want("a string or an object", raw)
	}}
}

func ShapeStringList() Shape {
	return Shape{Name: "list of strings", check: func(raw json.RawMessage) error {
		var l []string
		if json.Unmarshal(raw, &l) != nil || l == nil {
			return want("a list of strings", raw)
		}
		return nil
	}}
}

func ShapeEnum(values ...string) Shape { return enumShape(false, values) }

func ShapeEnumFold(values ...string) Shape { return enumShape(true, values) }

func enumShape(fold bool, values []string) Shape {
	name := "one of " + strings.Join(values, "|")
	if fold {
		name += " (any case)"
	}
	return Shape{Name: name, check: func(raw json.RawMessage) error {
		s, ok := asString(raw)
		if ok {
			if fold {
				s = strings.ToLower(strings.TrimSpace(s))
			}
			for _, v := range values {
				if s == v {
					return nil
				}
			}
		}
		return want("one of "+strings.Join(values, ", "), raw)
	}}
}

func ShapeInt(min, max int64) Shape {
	name := "integer"
	switch {
	case min > math.MinInt64 && max < math.MaxInt64:
		name = fmt.Sprintf("integer %d..%d", min, max)
	case min > math.MinInt64:
		name = fmt.Sprintf("integer >= %d", min)
	}
	return Shape{Name: name, check: func(raw json.RawMessage) error {
		var n int64
		if json.Unmarshal(raw, &n) != nil || isNull(raw) || n < min || n > max {
			return want("an "+name, raw)
		}
		return nil
	}}
}

func ShapeNumber(min, max float64, openMin bool) Shape {
	lo := "["
	if openMin {
		lo = "("
	}
	name := fmt.Sprintf("number in %s%g, %g]", lo, min, max)
	return Shape{Name: name, check: func(raw json.RawMessage) error {
		var f float64
		if json.Unmarshal(raw, &f) != nil || isNull(raw) || f < min || f > max || openMin && f == min {
			return want("a "+name, raw)
		}
		return nil
	}}
}

func scalar(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '{', '[', 'n':
		return false
	}
	return true
}

func ShapeTruthy() Shape {
	return Shape{Name: "flag (true/false, yes/no, on/off, 1/0, or a reason)", check: func(raw json.RawMessage) error {
		if !scalar(raw) {
			return want("a string, bool or number", raw)
		}
		return nil
	}}
}

func ShapeLifetime() Shape {
	return Shape{Name: "lifetime (Go duration, d, w; 0 for none)", check: func(raw json.RawMessage) error {
		if !scalar(raw) {
			return want("a duration", raw)
		}
		if _, err := ParseLifetime(ScalarString(raw)); err != nil {
			return want("a duration (90m, 6h, 2d)", raw)
		}
		return nil
	}}
}

func ShapeTimeout() Shape {
	return Shape{Name: `Go duration, or "0"/"off"`, check: func(raw json.RawMessage) error {
		s, ok := asString(raw)
		if !ok {
			return want(`a Go duration string, or "0" or "off"`, raw)
		}
		if s == "0" || s == "off" {
			return nil
		}
		if d, err := time.ParseDuration(s); err != nil || d < 0 {
			return want(`a Go duration string, or "0" or "off"`, raw)
		}
		return nil
	}}
}

func ShapeByteCount() Shape {
	return Shape{Name: "byte count (integer >= 0, or one as a string)", check: func(raw json.RawMessage) error {
		var n int
		if json.Unmarshal(raw, &n) == nil && !isNull(raw) && n >= 0 {
			return nil
		}
		if s, ok := asString(raw); ok {
			if n, err := strconv.Atoi(s); err == nil && n >= 0 {
				return nil
			}
		}
		return want("a byte count", raw)
	}}
}

func ShapeScopes() Shape {
	return Shape{Name: "list of verb:selector scopes", check: func(raw json.RawMessage) error { return checkScopeList(raw) }}
}

func ShapeRetirement() Shape {
	return Shape{Name: "retirement {context_threshold, fallback_tokens, ttl, action, page, timeout, ramp, ramp_turns}", check: func(raw json.RawMessage) error {
		if isNull(raw) {
			return nil
		}
		_, err := ParseRetirement(raw)
		return err
	}}
}

func ShapeIncantation(fields ...string) Shape {
	return Shape{Name: "string, or {" + strings.Join(fields, ", ") + "} of strings", check: func(raw json.RawMessage) error {
		return checkIncantation(raw, fields)
	}}
}

func checkIncantation(raw json.RawMessage, fields []string) error {
	if isNull(raw) {
		return nil
	}
	if _, ok := asString(raw); ok {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return want("a string or {"+strings.Join(fields, ", ")+"}", raw)
	}
	for name, value := range obj {
		if !containsString(fields, strings.ToLower(strings.TrimSpace(name))) {
			return fmt.Errorf("unknown field %q; want %s", name, strings.Join(fields, ", "))
		}
		if _, ok := asString(value); !ok && !isNull(value) {
			return fmt.Errorf("field %q: %w", name, want("a string", value))
		}
	}
	return nil
}

func CheckShape(path string, raw json.RawMessage) error {
	s, ok := ShapeOf(path)
	if !ok {
		return nil
	}
	return s.Check(raw)
}

func ShapeOf(path string) (Shape, bool) {
	for _, s := range registry {
		if !s.placeholder() && s.Path == path {
			return s.Shape, true
		}
	}
	for _, s := range registry {
		if s.placeholder() && placeholderMatches(s.Path, path) {
			return s.Shape, true
		}
	}
	return Shape{}, false
}

func placeholderMatches(pattern, path string) bool {
	ps, xs := strings.Split(pattern, "."), strings.Split(path, ".")
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if xs[i] == "" || strings.Contains(xs[i], "[") {
			return false
		}
		if ps[i] != xs[i] && !strings.HasPrefix(ps[i], "<") {
			return false
		}
	}
	return true
}

type ShapeFault struct {
	Path  string
	Value json.RawMessage
	Err   error
}

func AuditShapes(snap Snapshot) []ShapeFault {
	var out []ShapeFault
	check := func(path string, shape Shape) {
		raw, ok := snap.Get(path)
		if !ok {
			return
		}
		if err := shape.Check(raw); err != nil {
			out = append(out, ShapeFault{Path: path, Value: raw, Err: err})
		}
	}
	exact := map[string]bool{}
	for _, s := range registry {
		if !s.placeholder() {
			exact[s.Path] = true
			check(s.Path, s.Shape)
		}
	}
	for _, s := range registry {
		if !s.placeholder() {
			continue
		}
		n := len(strings.Split(s.Path, "."))
		seen := map[string]bool{}
		for leaf := range snap.All() {
			parts := strings.Split(leaf, ".")
			if len(parts) < n {
				continue
			}
			path := strings.Join(parts[:n], ".")
			if i := strings.IndexByte(path, '['); i >= 0 {
				continue
			}
			if seen[path] || exact[path] || !placeholderMatches(s.Path, path) {
				continue
			}
			seen[path] = true
			check(path, s.Shape)
		}
	}
	return out
}

var exactRows = func() map[string]bool {
	m := make(map[string]bool, len(registry))
	for _, s := range registry {
		if !s.placeholder() {
			m[s.Path] = true
		}
	}
	return m
}()

func TouchedShapeFaults(after Snapshot, touched []string) []ShapeFault {
	var out []ShapeFault
	seen := map[string]bool{}
	check := func(path string, shape Shape) {
		if seen[path] {
			return
		}
		seen[path] = true
		raw, ok := after.Get(path)
		if !ok {
			return
		}
		if err := shape.Check(raw); err != nil {
			out = append(out, ShapeFault{Path: path, Value: raw, Err: err})
		}
	}
	instance := func(row Spec, n int, leaf string) {
		path, ok := firstSegments(leaf, n)
		if ok && !exactRows[path] && placeholderMatches(row.Path, path) {
			check(path, row.Shape)
		}
	}
	for _, t := range touched {
		for _, s := range registry {
			if !s.placeholder() {
				if under(t, s.Path) || under(s.Path, t) {
					check(s.Path, s.Shape)
				}
				continue
			}
			n := strings.Count(s.Path, ".") + 1
			parent := s.Path[:strings.LastIndexByte(s.Path, '.')]
			switch {
			case strings.Count(t, ".")+1 >= n:
				instance(s, n, t)
			case under(parent, t):
				for leaf := range after.All() {
					if under(leaf, parent) && leaf != parent {
						instance(s, n, leaf)
					}
				}
			}
		}
	}
	return out
}

func firstSegments(path string, n int) (string, bool) {
	end := 0
	for i := 0; i < n; i++ {
		j := strings.IndexByte(path[end:], '.')
		if j < 0 {
			return path, i == n-1
		}
		if i == n-1 {
			return path[:end+j], true
		}
		end += j + 1
	}
	return "", false
}
