package angelus

func (w *ariasWatch) askRecent() {
	w.build.Lock()
	w.recent.asked = true
	w.build.Unlock()
}
