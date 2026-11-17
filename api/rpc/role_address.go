package rpc

import (
	"strconv"
	"strings"
)

func QuoteRoleAddress(address string) string {
	if address == "" || strings.ContainsFunc(address, func(r rune) bool {
		return r == '"' || r == '\\' || !strconv.IsPrint(r) || r == ' '
	}) {
		return strconv.Quote(address)
	}
	return address
}

func JoinRoleAddresses(addresses []string) string {
	quoted := make([]string, len(addresses))
	for i, a := range addresses {
		quoted[i] = QuoteRoleAddress(a)
	}
	return strings.Join(quoted, " ")
}
