package research

import (
	"strconv"
	"strings"
)

type fetchedIdentityMismatch struct{}

func (*fetchedIdentityMismatch) Error() string { return "source returned a different record identity" }

// ValidateFetchedRecord binds a single fetch to a record ID returned by search.
// It does not authenticate the publication, accept arbitrary provider aliases,
// or validate scientific quality. Check before lossy display normalization.
func ValidateFetchedRecord(sourceID, requestedID string, value Work) error {
	id := strings.TrimSpace(value.SourceRecordID)
	actualSource := strings.ToLower(strings.TrimSpace(value.SourceID))
	if actualSource != "" && actualSource != sourceID {
		return &SourceError{SourceID: sourceID, Code: FailureInvalidData, Message: "source returned a different record identity", Cause: &fetchedIdentityMismatch{}}
	}
	if !IsUsableSourceRecord(sourceID, value) {
		return &SourceError{SourceID: sourceID, Code: FailureInvalidData, Message: "source returned an invalid or incomplete fetched record"}
	}
	wanted, actual := CanonicalSourceRecordID(sourceID, requestedID), CanonicalSourceRecordID(sourceID, id)
	if wanted == "" || wanted != actual {
		return &SourceError{SourceID: sourceID, Code: FailureInvalidData, Message: "source returned a different record identity", Cause: &fetchedIdentityMismatch{}}
	}
	return nil
}

func fetchedRecordID(sourceID, value string) string {
	value = strings.TrimSpace(value)
	switch sourceID {
	case "crossref":
		return NormalizeDOI(value)
	case "openalex":
		const prefix = "https://openalex.org/"
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = value[len(prefix):]
		}
		return strings.ToUpper(value)
	case "eric":
		return strings.ToUpper(value)
	case "core":
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return ""
		}
		return strconv.FormatInt(id, 10)
	case "pubmed":
		for _, ch := range value {
			if ch < '0' || ch > '9' {
				return ""
			}
		}
		return strings.TrimLeft(value, "0")
	case "arxiv":
		return normalizeArXivRecordID(value)
	default:
		// Source record IDs are opaque, case-sensitive identities, not titles.
		return value
	}
}
