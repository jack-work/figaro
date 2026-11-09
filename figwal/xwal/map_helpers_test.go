package xwal

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MapPatch is the native nested-map patch: deep set/remove on a JSON object,
// addressed by path.
type MapPatch struct {
	Set    []MapSet   `json:"set,omitempty"`
	Remove [][]string `json:"remove,omitempty"`
}

// MapSet assigns Value (any JSON) at the nested Path, creating
// intermediate objects as needed.
type MapSet struct {
	Path  []string        `json:"path"`
	Value json.RawMessage `json:"value"`
}

// MapReducerName is the built-in reducer key for native nested maps; a
// reducible channel declared with it needs no caller registration.
const MapReducerName = "map"

// MapReducer is auto-available as "map"; exposed so a caller can register it
// under another name.
func MapReducer() Reducer { return Reducer{Fold: foldMap, Initial: []byte("{}")} }

// MapSetPatch builds (and validates) a patch setting value at path.
func MapSetPatch(path []string, value json.RawMessage) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("xwal: map set needs a non-empty path")
	}
	if !json.Valid(value) {
		return nil, fmt.Errorf("xwal: map set value is not valid JSON")
	}
	return json.Marshal(MapPatch{Set: []MapSet{{Path: path, Value: value}}})
}

// MapRemovePatch builds a patch removing the leaf at path.
func MapRemovePatch(path []string) ([]byte, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("xwal: map remove needs a non-empty path")
	}
	return json.Marshal(MapPatch{Remove: [][]string{path}})
}

func foldMap(state []byte, patches [][]byte) ([]byte, error) {
	root, err := decodeObject(state)
	if err != nil {
		return nil, fmt.Errorf("xwal: map state: %w", err)
	}
	for _, patch := range patches {
		var p MapPatch
		if len(patch) > 0 {
			if err := json.Unmarshal(patch, &p); err != nil {
				return nil, fmt.Errorf("xwal: map patch: %w", err)
			}
		}
		for _, s := range p.Set {
			if len(s.Path) == 0 {
				return nil, fmt.Errorf("xwal: map set with empty path")
			}
			v, err := decodeAny(s.Value)
			if err != nil {
				return nil, fmt.Errorf("xwal: map set value: %w", err)
			}
			deepSet(root, s.Path, v)
		}
		for _, path := range p.Remove {
			if len(path) == 0 {
				continue
			}
			deepRemove(root, path)
		}
	}
	return json.Marshal(root)
}

func decodeObject(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func decodeAny(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// deepSet assigns value at path, creating intermediate objects (and
// replacing a non-object intermediate with a fresh object).
func deepSet(root map[string]any, path []string, value any) {
	cur := root
	for i := 0; i < len(path)-1; i++ {
		next, ok := cur[path[i]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[path[i]] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

// deepRemove deletes the leaf at path if its parent chain exists.
func deepRemove(root map[string]any, path []string) {
	cur := root
	for i := 0; i < len(path)-1; i++ {
		next, ok := cur[path[i]].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, path[len(path)-1])
}
