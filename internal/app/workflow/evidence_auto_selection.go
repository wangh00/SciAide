package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func automaticEvidenceCitations(input json.RawMessage) ([]tool.CitationRef, bool) {
	var in struct {
		Candidates []tool.CitationRef `json:"candidates"`
		Screening  struct {
			Recommendation string   `json:"recommendation"`
			References     []string `json:"recommendedReferences"`
			Coverage       struct {
				Sufficient bool `json:"sufficientForClaimedScope"`
			} `json:"coverage"`
		} `json:"screening"`
	}
	if json.Unmarshal(input, &in) != nil || in.Screening.Recommendation != "proceed" || !in.Screening.Coverage.Sufficient {
		return nil, false
	}
	wanted := map[string]bool{}
	for _, r := range in.Screening.References {
		wanted[r] = true
	}
	selected := []tool.CitationRef{}
	for _, c := range in.Candidates {
		if wanted[c.Reference] {
			selected = append(selected, c)
			delete(wanted, c.Reference)
		}
	}
	if len(wanted) > 0 || len(selected) == 0 || validateCitationSubset(selected, in.Candidates) != nil {
		return nil, false
	}
	if auditCitationSelection(input, selected, false).RequiresLimitedAcceptance {
		return nil, false
	}
	return selected, true
}
func (s *RuntimeService) autoSelectEvidenceCitations(ctx context.Context, detail RunDetail, step Step, node CompiledNode) (bool, error) {
	if node.ID != "evidence_extraction" || node.Kind != NodeCitationSelection {
		return false, nil
	}
	enabled := false
	for _, n := range detail.Run.Compilation.Nodes {
		enabled = enabled || n.ID == "evidence_screening" && n.PromptVersion == selectedEvidenceVersion
	}
	if !enabled {
		return false, nil
	}
	if hashJSON(step.Input) != step.InputSHA256 {
		return true, fmt.Errorf("automatic citation input changed")
	}
	if code, err := s.validateRunSnapshots(ctx, detail); err != nil {
		return true, s.failBlockedStep(ctx, detail, code, err)
	}
	citations, ok := automaticEvidenceCitations(step.Input)
	if !ok {
		return false, nil
	}
	output := mustJSON(map[string]any{"citations": citations, "evidenceStatus": "verified_citations_selected", "selectionAudit": auditCitationSelection(step.Input, citations, false), "selectionMode": "automatic_recommendation"})
	next := step.Ordinal + 1
	final, outputs, err := finalOutputs(detail.Run, detail.Steps, step.ID, output, next)
	if err != nil {
		return true, err
	}
	event, err := s.event(detail.Run.ID, "workflow.citations_auto_selected", map[string]any{"stepId": step.ID, "count": len(citations)}, s.now())
	if err != nil {
		return true, err
	}
	return true, s.repository.CompleteStep(ctx, detail.Run.ID, step.ID, output, next, final, outputs, s.now(), event)
}
