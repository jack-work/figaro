package rpc

const MethodFormApply = "form.apply"

const CapFormApply Capability = "form.apply"

type FormApplyRequest struct {
	FigaroID       string   `json:"figaro_id"`
	Outfits        []string `json:"outfits"`
	Managed        []string `json:"managed"`
	IfVersion      uint64   `json:"if_version,omitempty"`
	ExpectedOutfit string   `json:"expected_outfit,omitempty"`
	Check          bool     `json:"check,omitempty"`
}

type FormApplyResponse struct {
	FigaroID string   `json:"figaro_id"`
	Version  uint64   `json:"version"`
	Outfit   string   `json:"outfit"`
	Outcome  string   `json:"outcome"`
	Set      []string `json:"set,omitempty"`
	Remove   []string `json:"remove,omitempty"`
}
