package netpolicy

import "testing"

func TestLooksLikeIPLiteralIncludesLegacyBrowserSpellings(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"127.0.0.1",
		"::1",
		"2130706433",
		"0177.0.0.1",
		"0x7f000001",
		"0x7f.0.0.1",
	} {
		if !LooksLikeIPLiteral(host) {
			t.Errorf("LooksLikeIPLiteral(%q) = false", host)
		}
	}
	for _, host := range []string{
		"example.com",
		"dead.beef",
		"123.example",
		"0xg.example",
	} {
		if LooksLikeIPLiteral(host) {
			t.Errorf("LooksLikeIPLiteral(%q) = true", host)
		}
	}
}
