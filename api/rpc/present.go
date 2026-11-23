package rpc

const MethodEndpointPresent = "endpoint.present"

type PresentRequest struct {
	Token string `json:"token"`
}

type PresentResponse struct {
	ExpiresMS int64 `json:"expires_ms"`
}
