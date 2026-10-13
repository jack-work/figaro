package transport

import (
	"fmt"
	"net/url"
)

func (e Endpoint) URI() string {
	switch e.Scheme {
	case "unix":
		return "unix://" + e.Address
	case "tcp":
		return "tcp://" + e.Address
	default:
		return e.Scheme + "://" + e.Address
	}
}

func (e Endpoint) String() string { return e.URI() }

func ParseEndpoint(uri string) (Endpoint, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return Endpoint{}, fmt.Errorf("parse endpoint %q: %w", uri, err)
	}
	switch u.Scheme {
	case "unix":
		return Endpoint{Scheme: "unix", Address: u.Path}, nil
	case "tcp":
		return Endpoint{Scheme: "tcp", Address: u.Host}, nil
	default:
		return Endpoint{Scheme: u.Scheme, Address: u.Host + u.Path}, nil
	}
}
