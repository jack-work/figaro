package rpc

import "strings"

type FormMode string

const (
	FormReadOnly FormMode = "read"
	FormWritable FormMode = "write"
)

type KeyDenyKind string

const (
	DenySystemManaged KeyDenyKind = "system-managed"
	DenyOwnerHeld     KeyDenyKind = "owner-held"
	DenyBoxHeld       KeyDenyKind = "box-held"
	DenyBoxUnlisted   KeyDenyKind = "box-unlisted"
	DenyProtected     KeyDenyKind = "protected"
)

type KeyDeny struct {
	Key    string      `json:"key"`
	Exact  bool        `json:"exact,omitempty"`
	Below  bool        `json:"below,omitempty"`
	Except []string    `json:"except,omitempty"`
	Kind   KeyDenyKind `json:"kind"`
	Reason string      `json:"reason"`
}

func (d KeyDeny) Denies(path string) bool {
	switch {
	case d.Exact:
		if path != d.Key {
			return false
		}
	case d.Below:
		if !strings.HasPrefix(path, d.Key) {
			return false
		}
	default:
		if !KeyUnder(path, d.Key) {
			return false
		}
	}
	for _, e := range d.Except {
		if KeyUnder(path, e) {
			return false
		}
	}
	return true
}

func KeyUnder(path, key string) bool {
	return path == key || strings.HasPrefix(path, key+".") || strings.HasPrefix(path, key+"[")
}

type FormAccess struct {
	Mode FormMode  `json:"mode"`
	Deny []KeyDeny `json:"deny,omitempty"`
}

func (a *FormAccess) Writable() bool { return a == nil || a.Mode != FormReadOnly }

func (a *FormAccess) Refusal(path string) (KeyDeny, bool) {
	if a == nil {
		return KeyDeny{}, false
	}
	for _, d := range a.Deny {
		if d.Denies(path) {
			return d, true
		}
	}
	return KeyDeny{}, false
}
