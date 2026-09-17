package research

import (
	"context"
	"encoding/json"
)

// Enrichment runs inside the registered search invocation, under its source
// permissions and audit. Search evidence is retained alongside fetched data.
func (s *Service) enrichMetadata(ctx context.Context, original Work) Work {
	fetched, err := s.Fetch(ctx, FetchCommand{SourceID: original.SourceID, RecordID: original.SourceRecordID})
	if err != nil {
		original.MetadataFetchStatus = "failed"
		_, original.MetadataFetchError, _ = classifyFailure(err)
		return original
	}
	if fetched.SourceRecordID != original.SourceRecordID || (original.Identifiers.DOI != "" && fetched.Identifiers.DOI != "" && NormalizeDOI(original.Identifiers.DOI) != NormalizeDOI(fetched.Identifiers.DOI)) {
		original.MetadataFetchStatus = "identity_mismatch"
		return original
	}
	original.MetadataFetchStatus = "unavailable"
	if fetched.Abstract != "" {
		original.Abstract = fetched.Abstract
		original.MetadataFetchStatus = "enriched"
	}
	// Missing fields can be filled, but a fetch must not silently replace
	// conflicting search metadata used to establish candidate identity.
	if len(original.Authors) == 0 {
		original.Authors = fetched.Authors
	}
	if original.Year == 0 {
		original.Year = fetched.Year
	}
	original.PublicationYears = append(original.PublicationYears, fetched.Year)
	original.PublicationYears = append(original.PublicationYears, fetched.PublicationYears...)
	if original.WorkType == "" {
		original.WorkType = fetched.WorkType
	}
	if original.PDFURL == "" {
		original.PDFURL = fetched.PDFURL
	}
	searchRaw, fetchRaw := original.RawSnapshot, fetched.RawSnapshot
	if !json.Valid(searchRaw) {
		searchRaw = json.RawMessage(`{}`)
	}
	if !json.Valid(fetchRaw) {
		fetchRaw = json.RawMessage(`{}`)
	}
	original.RawSnapshot, _ = json.Marshal(map[string]any{"search": searchRaw, "fetch": fetchRaw})
	return original
}
