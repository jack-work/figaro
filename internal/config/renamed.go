package config

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

type renamedKey struct {
	old     []string
	new     []string
	release string
}

var renamedKeys = []renamedKey{
	{old: []string{"slot"}, new: []string{"worker"}, release: "0.69"},
	{old: []string{"worker", "pools", "*", "slots"}, new: []string{"worker", "pools", "*", "workers"}, release: "0.69"},
	{old: []string{"door"}, new: []string{"endpoint"}, release: "0.70"},
	{old: []string{"sandbox", "boxes", "*", "door"}, new: []string{"sandbox", "boxes", "*", "endpoint"}, release: "0.70"},
	{old: []string{"sandbox", "boxes", "*", "host_door"}, new: []string{"sandbox", "boxes", "*", "host_endpoint"}, release: "0.70"},
}

func (r renamedKey) match(k toml.Key) (oldName, newName string, ok bool) {
	if len(k) < len(r.old) {
		return "", "", false
	}
	oldPath := make([]string, len(r.old))
	newPath := make([]string, len(r.new))
	for i, part := range r.old {
		switch {
		case part == "*":
			oldPath[i] = k[i]
		case part != k[i]:
			return "", "", false
		default:
			oldPath[i] = part
		}
	}
	for i, part := range r.new {
		if part == "*" {
			newPath[i] = k[i]
		} else {
			newPath[i] = part
		}
	}
	tail := k[len(r.old):]
	return strings.Join(append(oldPath, tail...), "."), strings.Join(append(newPath, tail...), "."), true
}

func refuseRenamedKeys(md toml.MetaData) error {
	for _, k := range md.Undecoded() {
		for _, r := range renamedKeys {
			if oldName, newName, ok := r.match(k); ok {
				return fmt.Errorf("%s was renamed %s in figaro %s; rename it in the file (nothing reads the old name)", oldName, newName, r.release)
			}
		}
	}
	return nil
}
