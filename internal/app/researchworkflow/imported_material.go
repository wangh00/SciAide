package researchworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

// Candidate IDs, not display titles, identify imported literature. The full
// normalized title remains in its source snapshot and candidate detail. This
// explicit view keeps a 100-material handoff within the executor envelope.
// User-selected attachment titles are not projected: publication uses those
// exact stored titles as part of the existing user-material provenance.
type ImportedTitlePreview struct {
	SHA256     string `json:"sha256"`
	TotalRunes int    `json:"totalRunes"`
	Truncated  bool   `json:"truncated"`
}

func importedCandidateMaterialView(value ImportedMaterial) ImportedMaterial {
	const maxTitleRunes = 96
	count := utf8.RuneCountInString(value.Title)
	if count <= maxTitleRunes {
		return value
	}
	sum := sha256.Sum256([]byte(value.Title))
	value.TitlePreview = &ImportedTitlePreview{SHA256: hex.EncodeToString(sum[:]), TotalRunes: count, Truncated: true}
	value.Title = string([]rune(value.Title)[:maxTitleRunes])
	return value
}
