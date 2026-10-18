package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type DoorConfig struct {
	Web *WebDoorConfig `toml:"web"`
	TLS *TLSDoorConfig `toml:"tls"`
}

type WebDoorConfig struct {
	Path       string `toml:"path"`
	Mode       string `toml:"mode"`
	MaxConnAge string `toml:"max_conn_age"`
	Authn      string `toml:"authn"`
}

type WebDoor struct {
	Path       string
	Mode       os.FileMode
	MaxConnAge time.Duration
	Authn      string
}

const (
	defaultWebDoorMode       = 0o660
	defaultWebDoorMaxConnAge = 8 * time.Hour
)

func (l *Loaded) WebDoor() (WebDoor, bool, error) {
	w := l.cfg().Door.Web
	if w == nil {
		return WebDoor{}, false, nil
	}
	if w.Path == "" {
		return WebDoor{}, false, fmt.Errorf("door.web: path is required")
	}
	out := WebDoor{Path: w.Path, Mode: defaultWebDoorMode, MaxConnAge: defaultWebDoorMaxConnAge, Authn: w.Authn}
	if w.Mode != "" {
		m, err := strconv.ParseUint(w.Mode, 8, 32)
		if err != nil || m > 0o777 {
			return WebDoor{}, false, fmt.Errorf("door.web: mode %q is not an octal permission", w.Mode)
		}
		out.Mode = os.FileMode(m)
	}
	if w.MaxConnAge != "" {
		d, err := time.ParseDuration(w.MaxConnAge)
		if err != nil || d <= 0 {
			return WebDoor{}, false, fmt.Errorf("door.web: max_conn_age %q is not a positive duration", w.MaxConnAge)
		}
		out.MaxConnAge = d
	}
	return out, true, nil
}
