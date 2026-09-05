package netpolicy

import (
	"net"
	"strings"
)

// LooksLikeIPLiteral reports both canonical IP literals and the legacy
// numeric IPv4 spellings accepted by WHATWG URL parsers and some clients.
// Security boundaries must reject these spellings before treating a host as
// a DNS name.
func LooksLikeIPLiteral(raw string) bool {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if strings.HasPrefix(part, "0x") {
			if len(part) == 2 || !allASCII(part[2:], isHexDigit) {
				return false
			}
			continue
		}
		if !allASCII(part, isDecimalDigit) {
			return false
		}
	}
	return true
}

func allASCII(value string, allowed func(byte) bool) bool {
	for index := 0; index < len(value); index++ {
		if !allowed(value[index]) {
			return false
		}
	}
	return true
}

func isDecimalDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isHexDigit(value byte) bool {
	return isDecimalDigit(value) ||
		(value >= 'a' && value <= 'f')
}
