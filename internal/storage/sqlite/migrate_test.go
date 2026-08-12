package sqlite

import "testing"

func TestLegacyBaselineChecksumIsNarrowlyAccepted(t *testing.T) {
	accepted := []string{
		"e9a66fd9fe954e369fb43f68be6a764ed35cbdbbc142bb6c5ec490954e69f3db",
		"ef8938a8fc66c530015ada08c0db37f3300abc1bdcd4166e8142021345287c7f",
	}
	for _, value := range accepted {
		if !legacyBaselineChecksum(value) {
			t.Fatalf("legacyBaselineChecksum(%q) = false", value)
		}
	}
	if legacyBaselineChecksum("changed") {
		t.Fatal("unrecognized checksum was accepted")
	}
}
