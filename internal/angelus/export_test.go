package angelus

func (a *Angelus) SweepLifetimesNow() { a.expireTTL() }

func SetMaxDoorFrameForTest(t interface{ Cleanup(func()) }, n int) {
	old := maxDoorFrame
	maxDoorFrame = n
	t.Cleanup(func() { maxDoorFrame = old })
}
