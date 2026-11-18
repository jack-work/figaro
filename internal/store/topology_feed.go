package store

type TopologyFeed struct {
	Born     func(id, parent string)
	Liveness func(id string, live bool)
	Limits   func(id string)
}

func (b *XwalBackend) Feed(f TopologyFeed) { b.feed.Store(&f) }

func (b *XwalBackend) EveryNode() []NodeView { return b.store.Nodes() }

func (b *XwalBackend) born(id string) {
	f := b.feed.Load()
	if f == nil || f.Born == nil {
		return
	}
	parent := rootID
	if t, ok := b.store.trunks.InfoLight(id); ok {
		switch {
		case t.Parent != "":
			parent = t.Parent
		case t.Stump != "":
			parent = t.Stump
		}
	}
	f.Born(id, parent)
}

func (b *XwalBackend) livenessChanged(id string, meta *AriaMeta) {
	if f := b.feed.Load(); f != nil && f.Liveness != nil {
		f.Liveness(id, meta.RetiredAtMS == 0 && meta.ReapedAtMS == 0)
	}
}

func (b *XwalBackend) limitsChanged(id string) {
	if f := b.feed.Load(); f != nil && f.Limits != nil {
		f.Limits(id)
	}
}
