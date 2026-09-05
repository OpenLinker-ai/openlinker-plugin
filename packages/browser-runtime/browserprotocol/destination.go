package browserprotocol

import (
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/netpolicy"
)

var blockedDestinationPrefixes = netpolicy.BlockedDestinationPrefixes()

func validateNavigateURL(raw string) *Failure {
	if raw == "" || len(raw) > 4096 {
		return NewFailure(ErrorProtocolInvalid, "navigate URL is empty or too large", false)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return NewFailure(ErrorProtocolInvalid, "navigate URL must be an HTTP(S) URL without credentials", false)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return NewFailure(ErrorProtocolInvalid, "navigate URL address is not public", false)
		}
		return NewFailure(ErrorProtocolInvalid, "navigate URL must use a public DNS hostname", false)
	}
	if netpolicy.LooksLikeIPLiteral(host) ||
		host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localhost") ||
		host == "metadata.google.internal" || strings.ContainsAny(host, " /\\@") {
		return NewFailure(ErrorProtocolInvalid, "navigate URL host is not public", false)
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedDestinationPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
