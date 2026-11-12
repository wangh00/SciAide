package research

import (
	"strings"
	"unicode/utf8"
)

// CanonicalSourceRecordID shares the source-ID equivalence used by Fetch.
// Identity is bounded by rejection, never truncation. This is not publication
// authentication or an arbitrary provider-alias resolver.
func CanonicalSourceRecordID(sourceID, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	id := fetchedRecordID(sourceID, value)
	if id == "" || utf8.RuneCountInString(id) > MaxRecordIDRunes {
		return ""
	}
	switch sourceID {
	case "openalex":
		if !strings.HasPrefix(id, "W") {
			return ""
		}
	case "semantic-scholar":
		if strings.ContainsAny(id, "?#") {
			return ""
		}
	case "arxiv":
		if strings.ContainsAny(id, "?#&=") {
			return ""
		}
	case "eric":
		if len(id) < 3 || len(id) > 14 || (id[:2] != "ED" && id[:2] != "EJ") {
			return ""
		}
		for _, ch := range id[2:] {
			if ch < '0' || ch > '9' {
				return ""
			}
		}
	}
	return id
}

// IsUsableSourceRecord is the common Search/Fetch minimum record contract.
// Optional source attribution must agree before any display normalization.
func IsUsableSourceRecord(sourceID string, value Work) bool {
	actual := strings.ToLower(strings.TrimSpace(value.SourceID))
	// Preserved record IDs must fit the public Fetch input, even when a longer
	// transport URL could be canonicalized. Converters may emit a lossless ID.
	return (actual == "" || actual == sourceID) && utf8.RuneCountInString(strings.TrimSpace(value.SourceRecordID)) <= MaxRecordIDRunes && CanonicalSourceRecordID(sourceID, value.SourceRecordID) != "" && collapseSpace(value.Title) != ""
}
