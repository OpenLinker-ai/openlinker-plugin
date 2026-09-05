package browserprotocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

const MaxMutationOrigins = 32

const RestrictedMutationOriginsSHA256 = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"

func CanonicalMutationOrigins(
	policy string,
	origins []string,
) ([]string, string, *Failure) {
	if origins == nil || len(origins) > MaxMutationOrigins {
		return nil, "", invalidMutationOrigins()
	}
	canonical := make([]string, 0, len(origins))
	seen := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		origin, ok := canonicalMutationOrigin(raw)
		if !ok {
			return nil, "", invalidMutationOrigins()
		}
		if _, exists := seen[origin]; exists {
			continue
		}
		seen[origin] = struct{}{}
		canonical = append(canonical, origin)
	}
	sort.Strings(canonical)
	switch policy {
	case "restricted":
		if len(canonical) != 0 {
			return nil, "", invalidMutationOrigins()
		}
	case "full":
		if len(canonical) == 0 {
			return nil, "", invalidMutationOrigins()
		}
	default:
		return nil, "", invalidMutationOrigins()
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", invalidMutationOrigins()
	}
	digest := sha256.Sum256(encoded)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func ValidateMutationOrigins(
	policy string,
	origins []string,
	digest string,
) *Failure {
	canonical, expected, failure := CanonicalMutationOrigins(policy, origins)
	if failure != nil || expected != digest || len(canonical) != len(origins) {
		return invalidMutationOrigins()
	}
	for index := range canonical {
		if canonical[index] != origins[index] {
			return invalidMutationOrigins()
		}
	}
	return nil
}

func canonicalMutationOrigin(raw string) (string, bool) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.Contains(raw, "%") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.Host == "" || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.ForceQuery {
		return "", false
	}
	host := parsed.Hostname()
	if host == "" || strings.HasSuffix(host, ".") {
		return "", false
	}
	port := parsed.Port()
	if port != "" {
		portNumber, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || portNumber == 0 {
			return "", false
		}
		if portNumber == 443 {
			port = ""
		} else {
			port = strconv.FormatUint(portNumber, 10)
		}
	}
	canonicalHost := ""
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if address.Is4In6() {
			return "", false
		}
		if address.Is6() {
			canonicalHost = "[" + address.String() + "]"
		} else {
			canonicalHost = address.String()
		}
	} else {
		ascii, idnaErr := idna.Lookup.ToASCII(strings.ToLower(host))
		if idnaErr != nil || !validMutationDomain(ascii) || ambiguousMutationIPv4Domain(ascii) {
			return "", false
		}
		canonicalHost = strings.ToLower(ascii)
	}
	if port != "" {
		canonicalHost += ":" + port
	}
	return "https://" + canonicalHost, true
}

// ambiguousMutationIPv4Domain rejects legacy WHATWG IPv4 spellings before
// Chromium can reinterpret a value that the authority layer treated as DNS.
func ambiguousMutationIPv4Domain(host string) bool {
	parts := strings.Split(strings.ToLower(host), ".")
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		digits := part
		hexadecimal := false
		if strings.HasPrefix(part, "0x") {
			digits = part[2:]
			hexadecimal = true
		}
		if digits == "" {
			return false
		}
		for index := 0; index < len(digits); index++ {
			character := digits[index]
			if character >= '0' && character <= '9' {
				continue
			}
			if hexadecimal && character >= 'a' && character <= 'f' {
				continue
			}
			return false
		}
	}
	return true
}

func validMutationDomain(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func invalidMutationOrigins() *Failure {
	return NewFailure(
		ErrorProtocolInvalid,
		"Browser interaction policy or mutation origins are invalid",
		false,
	)
}
