package research

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// A view is catalogue context, never a full-source or study-quality guarantee.
// Full normalized fields stay in the immutable query/source snapshot. Counts
// and its SHA identify omitted data; a preview is not proof a term is absent.
type SourceMetadataPreview struct {
	SHA256               string `json:"sha256"`
	Truncated            bool   `json:"truncated"`
	SubjectCount         int    `json:"subjectCount"`
	EducationLevelCount  int    `json:"educationLevelCount"`
	PublicationTypeCount int    `json:"publicationTypeCount"`
}

func PreviewSourceMetadata(value *SourceMetadata) (*SourceMetadata, *SourceMetadataPreview) {
	if value == nil {
		return nil, nil
	}
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	info := &SourceMetadataPreview{SHA256: hex.EncodeToString(sum[:]), SubjectCount: len(value.Subjects), EducationLevelCount: len(value.EducationLevels), PublicationTypeCount: len(value.PublicationTypes)}
	text := func(s string) string {
		if utf8.RuneCountInString(s) > 160 {
			info.Truncated = true
			return boundedText(s, 160)
		}
		return s
	}
	terms := func(values []string) []string {
		out := []string{}
		if len(values) > 2 {
			info.Truncated = true
			values = values[:2]
		}
		for _, term := range values {
			if utf8.RuneCountInString(term) > 64 {
				term = boundedText(term, 64)
				info.Truncated = true
			}
			out = append(out, term)
		}
		return out
	}
	out := *value
	out.Subjects = terms(value.Subjects)
	out.EducationLevels = terms(value.EducationLevels)
	out.PublicationTypes = terms(value.PublicationTypes)
	out.SourceCitation = text(value.SourceCitation)
	out.License = text(value.License)
	out.AccessNotice = text(value.AccessNotice)
	if value.PeerReviewed != nil {
		v := *value.PeerReviewed
		out.PeerReviewed = &v
	}
	return &out, info
}

type SourceMetadataRecord struct {
	SourceID       string                 `json:"sourceId"`
	SourceRecordID string                 `json:"sourceRecordId"`
	Metadata       *SourceMetadata        `json:"metadata"`
	Preview        *SourceMetadataPreview `json:"preview,omitempty"`
}

// Exact duplicate source versions are shared; distinct versions remain part of
// the full inventory/hash even when only one view per source fits this preview.
func SourceMetadataRecords(records []SourceRecord) []SourceMetadataRecord {
	result := []SourceMetadataRecord{}
	seen := map[string]bool{}
	for _, record := range records {
		if record.Work.SourceMetadata == nil {
			continue
		}
		encoded, _ := json.Marshal(record.Work.SourceMetadata)
		key := record.Work.SourceID + "\n" + record.Work.SourceRecordID + "\n" + string(encoded)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, SourceMetadataRecord{SourceID: record.Work.SourceID, SourceRecordID: record.Work.SourceRecordID, Metadata: record.Work.SourceMetadata})
	}
	return result
}

func PreviewSourceMetadataRecords(records []SourceMetadataRecord) ([]SourceMetadataRecord, int, string) {
	if len(records) == 0 {
		return nil, 0, ""
	}
	encoded, _ := json.Marshal(records)
	sum := sha256.Sum256(encoded)
	result := []SourceMetadataRecord{}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.SourceID] || len(result) == 2 {
			continue
		}
		seen[record.SourceID] = true
		record.Metadata, record.Preview = PreviewSourceMetadata(record.Metadata)
		result = append(result, record)
	}
	return result, len(records), hex.EncodeToString(sum[:])
}

// Import batches carry up to 100 materials. The handoff needs education-level
// context and review labels, not copies of subject/citation/licence prose from
// every source. Omitted fields remain bound by the full metadata/inventory SHA.
func ImportSourceMetadataRecords(records []SourceMetadataRecord) ([]SourceMetadataRecord, int, string) {
	views, total, hash := PreviewSourceMetadataRecords(records)
	for i := range views {
		metadata := views[i].Metadata
		info := views[i].Preview
		if len(metadata.Subjects) > 0 || len(metadata.PublicationTypes) > 0 || metadata.SourceCitation != "" || metadata.License != "" || metadata.AccessNotice != "" {
			info.Truncated = true
		}
		metadata.Subjects = nil
		metadata.PublicationTypes = nil
		metadata.SourceCitation = ""
		metadata.License = ""
		metadata.AccessNotice = ""
		if len(metadata.EducationLevels) > 1 {
			metadata.EducationLevels = metadata.EducationLevels[:1]
			info.Truncated = true
		}
		if len(metadata.EducationLevels) > 0 && utf8.RuneCountInString(metadata.EducationLevels[0]) > 48 {
			metadata.EducationLevels[0] = boundedText(metadata.EducationLevels[0], 48)
			info.Truncated = true
		}
	}
	return views, total, hash
}
