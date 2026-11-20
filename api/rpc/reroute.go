package rpc

const MethodReroute = "figaro.reroute"

type RerouteRequest struct {
	FigaroID string     `json:"figaro_id"`
	Outfits  []string   `json:"outfits"`
	Pins     *FormPatch `json:"pins,omitempty"`
}
