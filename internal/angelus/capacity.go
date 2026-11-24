package angelus

func (a *Angelus) PublishModelFacts() {
	for _, info := range a.Registry.List() {
		if p, ok := a.Registry.Get(info.ID).(interface{ PublishModelFacts() }); ok {
			p.PublishModelFacts()
		}
	}
}
