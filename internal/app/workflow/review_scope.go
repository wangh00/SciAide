package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const reviewScopeVersion = "review-scope-v4"

// These instructions change review reasoning, not the acceptance contract. In
// particular, the host must never delete a scientific finding or auto-approve.
const reviewScopeInstruction = `
Review responsibility and evidence channels (supersedes conflicting broad review guidance):
- Judge the CURRENT deliverable against researchContract.successCriteria at the maturity explicitly agreed for this delivery. originalRequest and routeContext preserve the research question and historical intent; when researchContract.deliveryScope exists, its confirmed current successCriteria takes precedence over previousCriteria for this delivery only. Do not reintroduce postponed conditions as additional gates, claim postponed work was completed, or waive factual/source/method checks. Without a confirmed deliveryScope, retain the original acceptance list; limited-evidence acceptance alone does not change it. A promised concrete draft must exist. Validation, ethics approval, data collection or future implementation explicitly deferred by the current agreement are not required now. Disclosing a limitation neither automatically passes nor automatically fails a deliverable.
- Review BEFORE downstream delivery gates. A criterion saying "after independent review and delivery verification" describes workflow order, not a requirement that the later gate already succeeded. Evaluate its current content requirements (consistency, provenance, no fabricated results). Do not mark it not_met or create a correction solely because this review or a downstream gate is pending. An actual false statement in the report that approval already occurred IS a defect; quote it. Never invent a prior approval.
- The host evidence inventory below indexes excerpts already supplied in stage_input.evidenceContext. Read their actual quote fields and compare each disputed report claim. These excerpts are available WITHOUT a duplicate read tool call. researchSourceContext.indexedReadRanges is an additional tool-read log, NOT an exhaustive inventory of supplied evidence or proof of model comprehension. Missing log entries alone do not invalidate supplied excerpts. Citation markers, hashes and counts alone do not prove scientific support either. Abstracts support only what they actually state, never full-text review, instrument validation or absence from an entire paper.
- For an explicit checkedScope/absence assertion, verify the exact source and bounded scope against the recorded reads required by that assertion. Do not generalize that special check to all positive claims or all citations. AI-derived evidenceScreening is synthesis to check against source excerpts, not independent source evidence.
- List one finding/correction per distinct actionable defect. Several acceptanceChecks may refer to the same finding IDs. An umbrella "core deliverables incomplete" criterion must reuse the specific missing-material findings, not duplicate them. Preserve separate genuinely different problems and unresolved historical finding identities; explain resolved or superseded historical findings against the current draft.
- Choose the latest offered repair stage capable of fully fixing the defect without leaving an incorrect reusable upstream result. Missing presentation of an already accepted correct evidence synthesis belongs in report drafting; missing design materials belong in research design. Reopen evidence screening only for a concrete synthesis/coverage defect, not merely to format an evidence table or repeat reads. Adoption-only gaps use the offered citation selection stage. whyNotLater must identify the actual upstream error, not assert generically that later editing cannot establish provenance. Use the preceding missing-information recovery guidance to distinguish accessible facts from genuinely missing user material or decisions; never promise that rerunning will create inaccessible evidence.
- 报告漏表格、筛选记录、可做的有限质量评估，不足以证明上游方法或综合有错；退回上游须定位具体错误/缺项。历史意见也需核验：真实问题保留，已改的resolved，原判不成立的withdrawn并说明当前依据/changeReason；保留ID，不扩大/降低原要求。
The following host-derived inventory is navigation and workflow metadata only, never scientific evidence. Its pointers refer to the unchanged frozen stage_input; no excerpts or criteria have been added or replaced.
`

type reviewExcerptLocation struct {
	Pointer string `json:"pointer"`
}

type reviewScopeInventory struct {
	Version                   string                  `json:"version"`
	CurrentNodeID             string                  `json:"currentNodeId"`
	DownstreamDeliveryGateIDs []string                `json:"downstreamDeliveryGateIds"`
	SuppliedExcerpts          []reviewExcerptLocation `json:"suppliedExcerpts"`
	AdditionalReadLogPointer  string                  `json:"additionalReadLogPointer,omitempty"`
	KernelResults             []reviewKernelLocation  `json:"kernelResults,omitempty"`
}

func independentReviewScope(detail RunDetail, step Step, node CompiledNode) string {
	if !isIndependentReviewSchema(node.OutputSchema) {
		return ""
	}
	inventory := reviewScopeInventory{
		Version: reviewScopeVersion, CurrentNodeID: node.ID,
		DownstreamDeliveryGateIDs: []string{}, SuppliedExcerpts: []reviewExcerptLocation{},
		KernelResults: reviewKernelLocations(step.Input),
	}
	// Only actual dependent gates from the compiled graph, never guessed names
	// or a prior attempt's status (which would misrepresent the current review).
	reachable := map[string]bool{node.ID: true}
	for changed := true; changed; {
		changed = false
		for _, edge := range detail.Run.Compilation.Edges {
			if reachable[edge.FromNode] && !reachable[edge.ToNode] {
				reachable[edge.ToNode] = true
				changed = true
			}
		}
	}
	for _, candidate := range detail.Run.Compilation.Nodes {
		if candidate.ID != node.ID && reachable[candidate.ID] && isReviewGateNode(candidate) {
			inventory.DownstreamDeliveryGateIDs = append(inventory.DownstreamDeliveryGateIDs, candidate.ID)
		}
	}
	var input struct {
		Evidence []struct {
			Quote string `json:"quote"`
		} `json:"evidenceContext"`
		Source struct {
			Reads json.RawMessage `json:"indexedReadRanges"`
		} `json:"researchSourceContext"`
	}
	if json.Unmarshal(step.Input, &input) == nil {
		for i, excerpt := range input.Evidence {
			if strings.TrimSpace(excerpt.Quote) != "" {
				inventory.SuppliedExcerpts = append(inventory.SuppliedExcerpts, reviewExcerptLocation{Pointer: fmt.Sprintf("/evidenceContext/%d/quote", i)})
			}
		}
		if len(input.Source.Reads) > 0 && string(input.Source.Reads) != "null" {
			inventory.AdditionalReadLogPointer = "/researchSourceContext/indexedReadRanges"
		}
	}
	return researchDeliveryBoundaryInstruction + reviewEvidenceFollowupInstruction + reviewScopeInstruction + string(mustJSON(inventory)) + "\n"
}
