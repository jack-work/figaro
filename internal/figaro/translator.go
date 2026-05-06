package figaro

import "log/slog"

// invalidateTranslatorIfStale clears the translator on any
// Fingerprint mismatch. The stream is a derivable cache.
func (a *Agent) invalidateTranslatorIfStale() {
	if a.translator == nil || a.prov == nil {
		return
	}
	want := a.prov.Fingerprint()
	if want == "" {
		return
	}
	for _, e := range a.translator.Durable() {
		if e.Fingerprint == "" || e.Fingerprint == want {
			continue
		}
		if err := a.translator.Clear(); err != nil {
			slog.Error("translator clear", "aria", a.id, "err", err)
			return
		}
		slog.Info("cleared stale translator (fingerprint mismatch)",
			"aria", a.id, "stored", e.Fingerprint, "current", want)
		return
	}
}
