package cli

import (
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

func ariaName(id string) string {
	if w := handle.Words(id, handle.MinWords); w != "" {
		return w
	}
	return id
}

func rowName(f *rpc.FigaroInfoResponse) string {
	if f.Handle != "" {
		return f.Handle
	}
	if f.Short != "" {
		return f.Short
	}
	return ariaName(f.ID)
}

func nameIfSet(names map[string]string, id string) string {
	if id == "" {
		return ""
	}
	return nameIn(names, id)
}
