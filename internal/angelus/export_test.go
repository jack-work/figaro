package angelus

func (a *Angelus) SweepLifetimesNow() { a.expireTTL() }
