package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Identity diagnostics are navigation, not permission to guess a paper from a
// similar UUID. The model explicitly chooses a frozen source; full validation
// still checks the selected source's original quote and scientific fields.
type LiteratureIdentityCell struct {
	Path       string   `json:"path"`
	Value      string   `json:"value"`
	AllowedIDs []string `json:"allowedIds"`
}
type LiteratureIdentitySource struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type LiteratureIdentityErrors struct {
	Problems []string                   `json:"problems"`
	Cells    []LiteratureIdentityCell   `json:"cells"`
	Sources  []LiteratureIdentitySource `json:"sources"`
	Local    bool                       `json:"local"`
}

func (e *LiteratureIdentityErrors) Error() string {
	b, _ := json.Marshal(e)
	return "literature candidate identity mismatch: " + string(b) + ". Use the exact frozen candidate ID in BOTH assessment and evidence note; do not guess by UUID similarity."
}

func validateLiteratureIdentities(output, input json.RawMessage) error {
	var in struct {
		Literature literatureInput       `json:"_literature"`
		Candidates []literatureCandidate `json:"candidates"`
	}
	var out struct {
		Assessments []literatureAssessment `json:"candidateAssessments"`
		Notes       []literatureNote       `json:"evidenceNotes"`
	}
	if json.Unmarshal(input, &in) != nil || in.Literature.Phase != "batch" || json.Unmarshal(output, &out) != nil {
		return nil
	}
	e := &LiteratureIdentityErrors{Local: true}
	offered := map[string]bool{}
	for _, c := range in.Candidates {
		offered[c.ID] = true
		e.Sources = append(e.Sources, LiteratureIdentitySource{ID: c.ID, Title: c.Title})
	}
	check := func(field string, ids []string) {
		seen := map[string]int{}
		unknown := []LiteratureIdentityCell{}
		for i, id := range ids {
			path := fmt.Sprintf("/%s/%d/candidateId", field, i)
			if first, ok := seen[id]; ok {
				e.Local = false
				e.Problems = append(e.Problems, fmt.Sprintf("%s duplicates /%s/%d/candidateId (%s)", path, field, first, id))
			} else {
				seen[id] = i
			}
			if !offered[id] {
				unknown = append(unknown, LiteratureIdentityCell{Path: path, Value: id})
				e.Problems = append(e.Problems, path+" is not in this batch: "+id)
			}
		}
		missing := []string{}
		for _, c := range in.Candidates {
			if _, ok := seen[c.ID]; !ok {
				missing = append(missing, c.ID)
			}
		}
		if len(missing) > 0 {
			e.Problems = append(e.Problems, field+" missing IDs: "+strings.Join(missing, ", "))
		}
		if len(unknown) != len(missing) || len(ids) != len(in.Candidates) {
			e.Local = false
		}
		for _, cell := range unknown {
			cell.AllowedIDs = append([]string(nil), missing...)
			e.Cells = append(e.Cells, cell)
		}
	}
	ids := []string{}
	for _, a := range out.Assessments {
		ids = append(ids, a.CandidateID)
	}
	check("candidateAssessments", ids)
	ids = nil
	for _, n := range out.Notes {
		ids = append(ids, n.CandidateID)
	}
	check("evidenceNotes", ids)
	if len(e.Problems) == 0 {
		return nil
	}
	if len(e.Cells) == 0 {
		e.Local = false
	}
	return e
}
