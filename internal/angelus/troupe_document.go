package angelus

import (
	"log/slog"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
)

func (a *Angelus) ApplyTroupeDocument(name string, body []byte, takeIssuers bool) error {
	err := a.Policies.SetTroupe(name, body)
	if a.Issuers == nil || !takeIssuers {
		return err
	}
	var issuers []config.IssuerConfig
	if body != nil && err == nil {
		if doc, perr := authz.ParseTroupeDocument(body); perr == nil {
			issuers = doc.Issuers
		}
	}
	if ierr := a.Issuers.SetTroupe(issuers); ierr != nil {
		slog.Error("troupe policy: issuers not taken up", "err", ierr)
	}
	return err
}
