package netpolicy

import (
	"net/netip"
	"testing"
)

func TestBlockedDestinationPrefixesReturnsAnImmutableCopy(t *testing.T) {
	t.Parallel()
	first := BlockedDestinationPrefixes()
	second := BlockedDestinationPrefixes()
	if len(first) != 28 || len(second) != len(first) {
		t.Fatalf("blocked prefix count = %d/%d, want 28", len(first), len(second))
	}
	first[0] = netip.MustParsePrefix("1.1.1.1/32")
	if second[0] != netip.MustParsePrefix("0.0.0.0/8") {
		t.Fatalf("shared blocked prefix data was mutable: %s", second[0])
	}
}
