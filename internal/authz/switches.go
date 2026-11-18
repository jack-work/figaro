package authz

import (
	"log/slog"
	"sync/atomic"

	"github.com/jack-work/figaro/internal/config"
)

type Switch int

const (
	KeyGrants Switch = iota
	ChildPolicy
	LineageReach
	Quota
	switchCount
)

var switchNames = [switchCount]string{"enforce_key_grants", "enforce_child_policy", "enforce_lineage_reach", "enforce_quota"}

func (s Switch) String() string { return switchNames[s] }

func Switches() []Switch { return []Switch{KeyGrants, ChildPolicy, LineageReach, Quota} }

type Enforcement struct {
	KeyGrants, ChildPolicy, LineageReach, Quota bool
}

func EnforcementOf(c config.AuthzConfig) Enforcement {
	return Enforcement{KeyGrants: c.EnforceKeyGrants, ChildPolicy: c.EnforceChildPolicy, LineageReach: c.EnforceLineageReach, Quota: c.EnforceQuota}
}

type enforcement struct {
	off   [switchCount]atomic.Bool
	would [switchCount]atomic.Uint64
}

func (p *Policies) SetEnforcement(e Enforcement) {
	p.enforce.off[KeyGrants].Store(!e.KeyGrants)
	p.enforce.off[ChildPolicy].Store(!e.ChildPolicy)
	p.enforce.off[LineageReach].Store(!e.LineageReach)
	p.enforce.off[Quota].Store(!e.Quota)
}

func (p *Policies) Enforces(s Switch) bool {
	return p == nil || !p.enforce.off[s].Load()
}

func (p *Policies) Shadow(s Switch, err error) error {
	if err == nil || p.Enforces(s) {
		return err
	}
	p.WouldRefuse(s, err.Error())
	return nil
}

func (p *Policies) WouldRefuse(s Switch, what string) {
	if p == nil {
		return
	}
	p.enforce.would[s].Add(1)
	slog.Info("authz: would refuse", "switch", s.String(), "what", what)
}

func (p *Policies) WouldRefused(s Switch) uint64 {
	if p == nil {
		return 0
	}
	return p.enforce.would[s].Load()
}
