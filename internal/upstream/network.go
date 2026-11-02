package upstream

import (
	"log/slog"
	"net"
)

const ReasonNetwork = "network"

func (p *pool) watchNetwork() {
	if p.watch == nil {
		return
	}
	p.watchOnce.Do(func() {
		if err := p.watch(p.recheck); err != nil {
			slog.Warn("provider connections will not follow network changes", "err", err)
		}
	})
}

func (p *pool) recheck() {
	for _, c := range p.live() {
		if !p.routeHolds(c) {
			c.retire(ReasonNetwork)
		}
	}
}

func (p *pool) routeHolds(c *conn) bool {
	local, ok := c.LocalAddr().(*net.TCPAddr)
	if !ok {
		return true
	}
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return true
	}
	src, err := p.source(remote)
	return err == nil && src.Equal(local.IP)
}

func kernelSource(remote *net.TCPAddr) (net.IP, error) {
	probe, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: remote.IP, Port: remote.Port, Zone: remote.Zone})
	if err != nil {
		return nil, err
	}
	defer probe.Close()
	return probe.LocalAddr().(*net.UDPAddr).IP, nil
}
