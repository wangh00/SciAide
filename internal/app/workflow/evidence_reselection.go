package workflow

import (
	"encoding/json"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

// Reopening selection creates a new attempt. It never edits the historical
// selection or synthesizes evidence that the accepted screening did not see.
func evidenceReselectionStep(d RunDetail) *Step {
	screen := findStepByNode(d.Steps, "evidence_screening")
	selection := findStepByNode(d.Steps, "evidence_extraction")
	if screen == nil || selection == nil || screen.Status != StepCompleted || selection.Status != StepCompleted || compilationNodeMap(d.Run.Compilation)[selection.NodeID].Kind != NodeCitationSelection {
		return nil
	}
	refs, _, ok := acceptedIndexedEvidence(d, *screen)
	if !ok || len(refs) == 0 || len(refs) > 256 || !workflowNodeReaches(d.Run.Compilation, screen.NodeID, selection.NodeID) {
		return nil
	}
	prior, err := outputPort(selection.Output, "citations")
	var selected []tool.CitationRef
	if err != nil || json.Unmarshal(prior, &selected) != nil || validateCitationSubset(selected, refs) != nil {
		return nil
	}
	return selection
}

type ResearchEvidenceReselection struct {
	NodeID                   string `json:"nodeId"`
	AvailableCount           int    `json:"availableCount"`
	RecommendedCount         int    `json:"recommendedCount"`
	SelectedRecommendedCount int    `json:"selectedRecommendedCount"`
	MissingRecommendedCount  int    `json:"missingRecommendedCount"`
	PreviouslyUnofferedCount int    `json:"previouslyUnofferedCount"`
	ReviewOutputSHA256       string `json:"reviewOutputSha256"`
}

func researchEvidenceReselection(d RunDetail) *ResearchEvidenceReselection {
	selection := evidenceReselectionStep(d)
	gate, review, err := rejectedDiscussionReview(d)
	if selection == nil || err != nil || !workflowNodeReaches(d.Run.Compilation, selection.NodeID, gate.NodeID) {
		return nil
	}
	result := evidenceSelectionAvailability(d)
	if result == nil || result.MissingRecommendedCount == 0 {
		return nil
	}
	result.ReviewOutputSHA256 = hashJSON(review)
	return result
}

func evidenceSelectionAvailability(d RunDetail) *ResearchEvidenceReselection {
	selection := evidenceReselectionStep(d)
	if selection == nil {
		return nil
	}
	screen := findStepByNode(d.Steps, "evidence_screening")
	refs, _, _ := acceptedIndexedEvidence(d, *screen)
	analysis, _ := outputPort(screen.Output, "analysis")
	var assessment struct {
		Recommended []string `json:"recommendedReferences"`
	}
	var offered struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	var selected []tool.CitationRef
	prior, _ := outputPort(selection.Output, "citations")
	_ = json.Unmarshal(analysis, &assessment)
	_ = json.Unmarshal(selection.Input, &offered)
	_ = json.Unmarshal(prior, &selected)
	available, previous, chosen := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, ref := range refs {
		available[ref.Reference] = true
	}
	for _, ref := range offered.Candidates {
		previous[ref.Reference] = true
	}
	for _, ref := range selected {
		chosen[ref.Reference] = true
	}
	result := &ResearchEvidenceReselection{NodeID: selection.NodeID, AvailableCount: len(refs)}
	seen := map[string]bool{}
	for _, ref := range assessment.Recommended {
		// An unbacked recommendation is a screening defect, not a selection fix.
		if !available[ref] {
			return nil
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		result.RecommendedCount++
		if chosen[ref] {
			result.SelectedRecommendedCount++
		} else {
			result.MissingRecommendedCount++
		}
		if !previous[ref] {
			result.PreviouslyUnofferedCount++
		}
	}
	return result
}

// Carry the prior choices into a fresh selection UI without authorizing any
// automatic selection. Full snapshots still come exclusively from screening.
func bindPreviousCitationSelection(d RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	if node.Kind != NodeCitationSelection {
		return input
	}
	var prior json.RawMessage
	var review *reviewRevision
	if r, ok := pendingReviewRevision(d, node.ID); ok {
		prior = r.PriorSubject
		review = &r
	}
	if p := pendingUserRevision(d, node.ID); p != nil {
		prior, _ = outputPort(priorUserRevisionOutput(d, node.ID), "citations")
		review = nil
	}
	var selected []tool.CitationRef
	if json.Unmarshal(prior, &selected) != nil {
		return input
	}
	v := decodeObject(input)
	refs := []string{}
	for _, ref := range selected {
		refs = append(refs, ref.Reference)
	}
	v["previousSelectedReferences"] = refs
	if review != nil {
		var offered struct {
			Candidates []tool.CitationRef `json:"candidates"`
		}
		if json.Unmarshal(input, &offered) == nil {
			v["citationRepairContext"] = citationRepairContext(*review, offered.Candidates)
		}
	}
	return mustJSON(v)
}

type citationSelectionRepair struct {
	ReviewOutputSHA256  string   `json:"reviewOutputSha256"`
	RequiredCorrections []string `json:"requiredCorrections"`
	MentionedReferences []string `json:"mentionedReferences"`
	RepeatedIssueCount  int      `json:"repeatedIssueCount"`
}

// Advisory UI context from the already confirmed review, not another plan or
// a scientific completeness test. Only exact references offered in this new
// attempt can be highlighted; they are never selected or authorized here.
func citationRepairContext(r reviewRevision, offered []tool.CitationRef) citationSelectionRepair {
	var review struct {
		Corrections []string               `json:"requiredCorrections"`
		Findings    []trackedReviewFinding `json:"reviewFindings"`
	}
	_ = json.Unmarshal(r.IndependentReview, &review)
	result := citationSelectionRepair{ReviewOutputSHA256: r.ReviewOutputSHA256, RequiredCorrections: review.Corrections, MentionedReferences: []string{}}
	texts := append([]string{}, review.Corrections...)
	for _, finding := range review.Findings {
		if finding.Status != "open" {
			continue
		}
		if finding.Origin == "existing" || finding.Origin == "reopened" {
			result.RepeatedIssueCount++
		}
		texts = append(texts, finding.Summary, finding.Reason, finding.ChangeReason)
		for _, basis := range finding.Basis {
			texts = append(texts, basis.Quote)
		}
	}
	mentioned := strings.Join(texts, "\n")
	seen := map[string]bool{}
	for _, ref := range offered {
		if ref.Reference != "" && !seen[ref.Reference] && strings.Contains(mentioned, ref.Reference) {
			result.MentionedReferences = append(result.MentionedReferences, ref.Reference)
			seen[ref.Reference] = true
		}
	}
	return result
}
