package cli

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
)

type idStyle uint8

const (
	idsAsHandles idStyle = iota
	idsAsShortHex
)

var shownIDs = idsAsHandles

func parseIDStyle(s string) (idStyle, error) {
	switch s {
	case "", "handle":
		return idsAsHandles, nil
	case "short":
		return idsAsShortHex, nil
	}
	return idsAsHandles, fmt.Errorf("[cli] ids = %q: want \"handle\" or \"short\"", s)
}

func applyIDStyle(loaded *config.Loaded) error {
	style, err := parseIDStyle(loaded.IDs())
	shownIDs = style
	return err
}

func ariaName(id string) string {
	if shownIDs == idsAsShortHex {
		return shortHex(id)
	}
	return ariaHandle(id)
}

func ariaHandle(id string) string {
	if w := handle.Words(id, handle.MinWords); w != "" {
		return w
	}
	return id
}

func shortHex(id string) string {
	body := strings.TrimPrefix(id, "@")
	if !rpc.IsHexID(id) || len(body) < rpc.OldIDLen {
		return id
	}
	return id[:len(id)-len(body)+rpc.ShortIDLen]
}

func rowName(f *rpc.FigaroInfoResponse) string {
	if shownIDs == idsAsHandles && f.Handle != "" {
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

type displayedRow struct {
	*rpc.FigaroInfoResponse
	Display string `json:"display"`
}

func displayed(f *rpc.FigaroInfoResponse) displayedRow {
	return displayedRow{FigaroInfoResponse: f, Display: rowName(f)}
}
