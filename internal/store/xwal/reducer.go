package xwal

func resolveReducer(cfg Config, name, channel string) (Reducer, bool) {
	if cfg.Registry != nil {
		if r, ok := cfg.Registry[name]; ok {
			return r, true
		}
		if r, ok := cfg.Registry[channel]; ok {
			return r, true
		}
	}
	return Reducer{}, false
}
