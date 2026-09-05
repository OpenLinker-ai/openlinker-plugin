package browserprotocol

import "strings"

// IsValidUUID checks the canonical nonzero UUID encoding used on Browser wires.
func IsValidUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}

func validOpaqueID(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
