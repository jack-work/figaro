package form

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const PatchShape = `a patch is {"Object":{"Set":{"key":value}}}, {"Object":{"Update":{"key":{"Scalar":{"Before":old,"After":new}}}}}, {"Object":{"Delete":{"key":old}}} or {"Scalar"|"List":...}`

func CheckPatchJSON(raw json.RawMessage) error {
	if t := bytes.TrimSpace(raw); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	return checkPatchShape(raw, "patch")
}

func checkPatchShape(raw json.RawMessage, at string) error {
	fields, err := shapeFields(raw, at)
	if err != nil {
		return err
	}
	for k, v := range fields {
		switch strings.ToLower(k) {
		case "scalar":
			if _, err := shapeFields(v, at+".Scalar", "before", "after"); err != nil {
				return err
			}
		case "object":
			if err := checkNested(v, at+".Object", "update", "set", "delete", "new"); err != nil {
				return err
			}
		case "list":
			if err := checkNested(v, at+".List", "update", "create", "delete", "order"); err != nil {
				return err
			}
		default:
			return shapeError(at, k)
		}
	}
	return nil
}

func checkNested(raw json.RawMessage, at string, nested string, plain ...string) error {
	fields, err := shapeFields(raw, at, append(plain, nested)...)
	if err != nil {
		return err
	}
	for k, v := range fields {
		if !strings.EqualFold(k, nested) {
			continue
		}
		var children map[string]json.RawMessage
		if err := json.Unmarshal(v, &children); err != nil {
			return fmt.Errorf("%s.Update: %w; %s", at, err, PatchShape)
		}
		for key, child := range children {
			if err := checkPatchShape(child, at+".Update."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func shapeFields(raw json.RawMessage, at string, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("%s is not an object: %w; %s", at, err, PatchShape)
	}
	if allowed == nil {
		return fields, nil
	}
	for k := range fields {
		ok := false
		for _, a := range allowed {
			ok = ok || strings.EqualFold(k, a)
		}
		if !ok {
			return nil, shapeError(at, k)
		}
	}
	return fields, nil
}

func shapeError(at, field string) error {
	return fmt.Errorf("%s: %q is not a patch field; %s", at, field, PatchShape)
}
