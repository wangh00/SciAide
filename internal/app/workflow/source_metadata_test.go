package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	research "github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"testing"
)

func TestLiteratureMetadataProjectionKeepsFullFingerprintAndScopedFields(t *testing.T) {
	yes := true
	metadata := &research.SourceMetadata{Subjects: []string{}, EducationLevels: []string{"Higher Education"}, PeerReviewed: &yes}
	for i := 0; i < 100; i++ {
		metadata.Subjects = append(metadata.Subjects, strings.Repeat("题", 300))
	}
	work := research.Work{SourceID: "eric", SourceRecordID: "EJ1234567", Title: "Education fixture", Abstract: "No claim from subject labels", SourceMetadata: metadata}
	candidates := literatureCandidates([]research.Candidate{{ID: "one", Preferred: research.Work{SourceID: "crossref", Title: "Education fixture"}, Records: []research.SourceRecord{{Work: work}, {Work: work}}}})
	if len(candidates) != 1 || len(candidates[0].SourceMetadata) != 1 || len(candidates[0].SourceMetadata[0].Metadata.Subjects) != 100 {
		t.Fatal("full source inventory lost")
	}
	full, _ := json.Marshal(candidates[0])
	view := candidates[0]
	view.SourceMetadata, view.SourceMetadataTotalRecords, view.SourceMetadataSHA256 = research.PreviewSourceMetadataRecords(view.SourceMetadata)
	preview, _ := json.Marshal(view)
	if len(preview) > 100*1024 || len(full) <= len(preview) || view.SourceMetadata[0].Metadata.EducationLevels[0] != "Higher Education" || view.SourceMetadataSHA256 == "" {
		t.Fatal("metadata alone prevents screening", len(full), len(preview))
	}
	if len(candidates[0].SourceMetadata[0].Metadata.Subjects) != 100 {
		t.Fatal("full fingerprint basis changed")
	}
}

func TestLongMetadataScreenFinalizeSelectionAndExactReadback(t *testing.T) {
	s, repo, reader, detail, node := literatureFixture(t, 4)
	for i := range reader.values {
		metadata := &research.SourceMetadata{}
		for j := 0; j < 100; j++ {
			term := fmt.Sprintf("%03d%s", j, strings.Repeat("题", 297))
			metadata.Subjects = append(metadata.Subjects, term)
			metadata.EducationLevels = append(metadata.EducationLevels, term)
			metadata.PublicationTypes = append(metadata.PublicationTypes, term)
		}
		work := reader.values[i].Preferred
		work.SourceID = "eric"
		work.SourceRecordID = fmt.Sprintf("EJ123456%d", i)
		work.SourceMetadata = metadata
		reader.values[i].Preferred = work
		reader.values[i].Records = []research.SourceRecord{{Work: work}}
	}
	original, _ := json.Marshal(literatureCandidates(reader.values))
	if len(original) < 1024*1024 {
		t.Fatal("fixture does not exercise old selection budget", len(original))
	}
	input, err := s.prepareLiteratureInput(context.Background(), detail, node, raw(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Candidates []literatureCandidate `json:"candidates"`
	}
	if err = json.Unmarshal(input, &in); err != nil || len(in.Candidates) != 4 {
		t.Fatal(err)
	}
	assessments := []literatureAssessment{}
	notes := []map[string]any{}
	ids := []string{}
	for _, c := range in.Candidates {
		ids = append(ids, c.ID)
		assessments = append(assessments, literatureAssessment{CandidateID: c.ID, Decision: "core", Relevance: "high", Reason: "Source grounded", ImportAction: "direct", Purpose: "Contribution"})
		notes = append(notes, map[string]any{"candidateId": c.ID, "quotes": fixtureLiteratureNote(c).Quotes})
	}
	output := mustJSON(map[string]any{"phase": "batch", "summary": "screened", "candidateAssessments": assessments, "evidenceNotes": notes})
	phase := literaturePhaseNode(node, input)
	if err = (tool.JSONSchemaValidator{}).Validate(phase.OutputSchema, output); err != nil {
		t.Fatal(err)
	}
	if err = validateWorkflowAIStageOutput(phase, output, input); err != nil {
		t.Fatal(err)
	}
	execution := AIExecution{ID: "screened", Status: "completed", Output: output}
	step := detail.Steps[1]
	step.Input = input
	if err = s.completeLiteratureScreening(context.Background(), detail, step, node, execution, output); err != nil {
		t.Fatal(err)
	}
	detail.Events = append(detail.Events, repo.event)
	detail.AIExecutions = append(detail.AIExecutions, execution)
	analysis := map[string]any{"recommendedCandidateIds": ids}
	if err = s.finalizeLiteratureEvidence(context.Background(), detail, literatureInput{State: literatureState(detail)}, &analysis); err != nil {
		t.Fatal(err)
	}
	kept := analysis["selectionCandidates"].([]literatureCandidate)
	if len(kept) != 4 || kept[0].SourceMetadataSHA256 == "" || kept[0].SourceSHA256 == "" {
		t.Fatal("selection lost source anchors")
	}
	detail.Steps[1].Status = StepCompleted
	detail.Steps[1].Output = mustJSON(map[string]any{"candidates": kept})
	selection, err := s.prepareLiteratureInput(context.Background(), detail, CompiledNode{ID: "candidate_review"}, raw(`{}`))
	if err != nil || len(selection) > 1024*1024 {
		t.Fatal("final selection exceeds budget", err, len(selection))
	}
	detail.Steps[2].Input = selection
	s.repository = &literatureReadRepository{detail: detail}
	full, err := s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var restored literatureCandidate
	if json.Unmarshal(full, &restored) != nil || len(restored.SourceMetadata[0].Metadata.Subjects) != 100 || restored.SourceMetadata[0].Preview != nil {
		t.Fatal("readback replaced full frozen data with preview")
	}
	reader.values[0].Records[0].Work.SourceMetadata.Subjects[99] = "changed omitted term"
	if _, err = s.ReadLiteratureCandidate(context.Background(), "project", "run", "review", ids[0]); err == nil {
		t.Fatal("hidden metadata version change bypassed source hash")
	}
}
