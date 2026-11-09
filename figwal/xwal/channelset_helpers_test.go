package xwal

import "fmt"

func (x *XWAL) addChannel(spec ChannelSpec) error {
	if x.channels().byName[spec.Name] != nil {
		return fmt.Errorf("xwal: channel %q already exists", spec.Name)
	}
	if err := registerChannel(x.root, x.cfg, spec); err != nil {
		return err
	}
	x.cfg = withChannelSpec(x.cfg, spec)
	return x.publish(spec.manifest())
}

func (x *XWAL) publish(mc manifestChannel) error {
	x.cs.mu.Lock()
	defer x.cs.mu.Unlock()
	return x.publishLocked(mc)
}

func withChannelSpec(cfg Config, spec ChannelSpec) Config {
	channels := append([]ChannelSpec(nil), cfg.Channels...)
	for i := range channels {
		if channels[i].Name == spec.Name {
			channels[i] = spec
			cfg.Channels = channels
			return cfg
		}
	}
	cfg.Channels = append(channels, spec)
	return cfg
}
