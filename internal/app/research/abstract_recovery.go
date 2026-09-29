package research

import (
	"encoding/json"
	"strings"

	"github.com/wangh00/SciAide/internal/researchtext"
)

// RecoverCandidateAbstracts is a derived projection, not a database migration.
// Restore only the exact legacy-cleaner result from an identity-matching raw
// record. Existing source records, attachments and issued citations stay intact.
func RecoverCandidateAbstracts(candidate Candidate) (Candidate, bool) {
	changed := false
	candidate.Records = append([]SourceRecord(nil), candidate.Records...)
	for i := range candidate.Records {
		w := &candidate.Records[i].Work
		var r struct {
			ID           string `json:"id"`
			Source       string `json:"source"`
			DOI          string `json:"DOI"`
			Abstract     string `json:"abstract"`
			AbstractText string `json:"abstractText"`
		}
		if len(w.RawSnapshot) > MaxRawSnapshotBytes || json.Unmarshal(w.RawSnapshot, &r) != nil {
			continue
		}
		var source string
		switch w.SourceID {
		case "europepmc":
			if r.ID == "" || r.Source+"/"+r.ID != w.SourceRecordID {
				continue
			}
			source = r.AbstractText
		case "crossref":
			if r.DOI == "" || !strings.EqualFold(r.DOI, w.SourceRecordID) {
				continue
			}
			source = r.Abstract
		default:
			continue
		}
		fixed := boundedText(researchtext.Plain(source), 20_000)
		if fixed == "" || fixed == w.Abstract || boundedText(researchtext.Legacy(source), 20_000) != w.Abstract {
			continue
		}
		if candidate.Preferred.SourceID == w.SourceID && candidate.Preferred.SourceRecordID == w.SourceRecordID && candidate.Preferred.Abstract == w.Abstract {
			candidate.Preferred.Abstract = fixed
		}
		w.Abstract = fixed
		changed = true
	}
	return candidate, changed
}
