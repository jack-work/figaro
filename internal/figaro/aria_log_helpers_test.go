package figaro_test

import (
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

func contains(s, sub string) bool    { return strings.Contains(s, sub) }
func rpcQua(t string) rpc.QuaRequest { return rpc.QuaRequest{Text: t} }
