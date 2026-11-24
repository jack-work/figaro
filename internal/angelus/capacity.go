package angelus

func (a *Angelus) PublishContextLimits() {
	for _, info := range a.Registry.List() {
		if p, ok := a.Registry.Get(info.ID).(interface{ PublishContextLimit() }); ok {
			p.PublishContextLimit()
		}
	}
}
