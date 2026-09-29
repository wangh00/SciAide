package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/tool"
	"strings"
	"time"
)

func (s *RuntimeService) reuseSelectedMaterials(ctx context.Context, d RunDetail, step Step, node CompiledNode) (bool, error) {
	if node.ID != "candidate_review" || node.Kind != NodeCandidateSelection {
		return false, nil
	}
	var in struct {
		Reuse      bool                          `json:"reuseSelectedMaterials"`
		References []attachment.MessageReference `json:"referenceMaterials"`
		Candidates []json.RawMessage             `json:"candidates"`
	}
	if json.Unmarshal(step.Input, &in) != nil || !in.Reuse || len(in.References) == 0 || len(in.Candidates) != 0 {
		return false, nil
	}
	if hashJSON(step.Input) != step.InputSHA256 {
		return true, fmt.Errorf("material selection snapshot changed")
	}
	if code, err := s.validateRunSnapshots(ctx, d); err != nil {
		return true, fmt.Errorf("%s: %w", code, err)
	}
	if s.materialLoader == nil {
		return true, fmt.Errorf("material loader unavailable")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, ref := range in.References {
		if ref.AttachmentID == "" || seen[ref.AttachmentID] {
			return true, fmt.Errorf("invalid selected material identity")
		}
		ids = append(ids, ref.AttachmentID)
		seen[ref.AttachmentID] = true
	}
	if selector, ok := s.materialLoader.(interface {
		SelectReferenceMaterials(context.Context, string, string, []string) ([]attachment.Attachment, error)
	}); ok {
		if _, err := selector.SelectReferenceMaterials(ctx, d.Run.ProjectID, d.Run.ResearchTaskID, ids); err != nil {
			return true, err
		}
	}
	materials, err := s.materialLoader.ReferenceMaterials(ctx, d.Run.ProjectID, d.Run.ResearchTaskID, ids)
	if err != nil {
		return true, err
	}
	if len(materials) != len(ids) {
		return true, fmt.Errorf("selected materials are missing")
	}
	for _, m := range materials {
		if !seen[m.ID] || m.Status != attachment.StatusReady {
			return true, fmt.Errorf("selected material is not ready")
		}
		delete(seen, m.ID)
	}
	audit := candidateSelectionAudit(step.Input, nil)
	audit.SelectedAttachmentCount = len(materials)
	output := mustJSON(map[string]any{"selectedCandidateIds": []string{}, "selectedAttachmentIds": ids, "selectedMaterials": materials, "selectionAudit": audit, "selectionMode": "reuse_user_selection"})
	next := step.Ordinal + 1
	final, outputs, err := finalOutputs(d.Run, d.Steps, step.ID, output, next)
	if err != nil {
		return true, err
	}
	event, err := s.event(d.Run.ID, "workflow.materials_reused", map[string]any{"stepId": step.ID, "attachmentIds": ids}, s.now())
	if err != nil {
		return true, err
	}
	return true, s.repository.CompleteStep(ctx, d.Run.ID, step.ID, output, next, final, outputs, s.now(), event)
}

type localEvidenceContinuationRepository interface {
	QueueLocalEvidenceSearch(context.Context, string, string, string, int, time.Time, RuntimeEvent) error
}

// Persist bounded queries, never model-supplied document identities. Re-run only
// search and screening, preserving import/sync and every historical tool result.
func supplementaryEvidenceQueries(d RunDetail) ([]string, int) {
	queries := []string{}
	rounds := 0
	for _, ev := range d.Events {
		if ev.Type != "workflow.local_evidence_requested" {
			continue
		}
		var v struct {
			Queries     []string `json:"queries"`
			MaterialKey string   `json:"materialKey"`
		}
		if json.Unmarshal(ev.Payload, &v) == nil && v.MaterialKey == localEvidenceMaterialKey(d) {
			rounds++
			queries = appendUnique(queries, v.Queries...)
		}
	}
	return queries, rounds
}
func bindSupplementaryEvidenceQueries(d RunDetail, args map[string]any) {
	queries, _ := supplementaryEvidenceQueries(d)
	if len(queries) > 0 {
		if len(queries) > 8 {
			queries = queries[:8]
		}
		// Keep all eight bounded follow-ups plus at most three initial queries (eleven supplementary queries total).
		base := []string{}
		_ = json.Unmarshal(mustJSON(args["queries"]), &base)
		if len(base) > 3 {
			base = base[:3]
		}
		args["queries"] = mustJSON(appendUnique(base, queries...))
	}
}
func (s *RuntimeService) supplementLocalEvidence(ctx context.Context, d RunDetail, step Step, output json.RawMessage) (bool, error) {
	search := findStepByNode(d.Steps, "evidence_search")
	if search == nil || search.Status != StepCompleted || step.Ordinal != search.Ordinal+1 {
		return false, nil
	}
	var settings struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(search.Input, &settings)
	if settings.Limit != 20 {
		return false, nil
	} // New plans only.
	var out struct {
		Queries  []string      `json:"supplementalQueries"`
		Gaps     []evidenceGap `json:"evidenceGaps"`
		Coverage struct {
			Sufficient bool     `json:"sufficientForClaimedScope"`
			Gaps       []string `json:"gaps"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return false, err
	}
	previous, rounds := supplementaryEvidenceQueries(d)
	if rounds >= 2 {
		return false, nil
	}
	// A sufficient overall assessment must not hide missing optional fields.
	// Explicit structured gaps take priority over broad model queries.
	out.Queries = appendUnique(gapFollowupQueries(out.Gaps, out.Coverage.Gaps, previous), out.Queries...)
	// A missing model query must not silently skip local follow-up on insufficient coverage.
	if !out.Coverage.Sufficient && len(out.Queries) == 0 && rounds == 0 {
		out.Queries = []string{"results outcomes estimates uncertainty", "population inclusion age eligibility methods", "confidence evidence quality limitations"}
	}
	queries := []string{}
	for _, q := range out.Queries {
		q = strings.TrimSpace(q)
		if q == "" || len([]rune(q)) > 200 {
			continue
		}
		if !containsQuery(previous, q) {
			queries = appendUnique(queries, q)
		}
		if len(queries) == 4 {
			break
		}
	}
	if len(queries) == 0 {
		return false, nil
	}
	repo, ok := s.repository.(localEvidenceContinuationRepository)
	if !ok {
		return true, fmt.Errorf("local evidence continuation unavailable")
	}
	var priorSnapshot struct {
		Citations []tool.CitationRef `json:"citations"`
	}
	if err := json.Unmarshal(search.Output, &priorSnapshot); err != nil {
		return true, err
	}
	previousOutput := mustJSON(priorSnapshot)
	if len(priorSnapshot.Citations) > 240 || len(previousOutput) > 200*1024 {
		return true, fmt.Errorf("local evidence snapshot exceeds bounded continuation budget")
	}
	event, err := s.event(d.Run.ID, "workflow.local_evidence_requested", map[string]any{"stepId": step.ID, "attempt": step.Attempt, "queries": queries, "materialKey": localEvidenceMaterialKey(d), "previousSearchOutput": previousOutput, "previousSearchHash": hashJSON(previousOutput), "round": rounds + 1, "message": "正在所选材料内补查关键原文，不新增文献、不联网。"}, s.now())
	if err != nil {
		return true, err
	}
	return true, repo.QueueLocalEvidenceSearch(ctx, d.Run.ID, search.ID, step.ID, step.Attempt, s.now(), event)
}
func localEvidenceMaterialKey(d RunDetail) string {
	if sync := findStepByNode(d.Steps, "evidence_sync"); sync != nil && sync.Status == StepCompleted {
		return hashJSON(sync.Output)
	}
	return ""
}
func containsQuery(v []string, q string) bool {
	for _, s := range v {
		if s == q {
			return true
		}
	}
	return false
}

// Preserve previously issued evidence across local follow-ups. The result is
// still validated against the successful original tool calls by citation seeding.
func mergeLocalEvidenceResults(d RunDetail, step Step, result tool.Result) (tool.Result, error) {
	if step.NodeID != "evidence_search" {
		return result, nil
	}
	merged := []tool.CitationRef{}
	seen := map[string]bool{}
	limited := false
	bytes := 0
	docBytes := map[string]int{}
	add := func(cs []tool.CitationRef) {
		for _, c := range cs {
			key := c.IndexVersionID + "/" + c.ChunkID + "/" + c.QuoteSHA256
			if seen[key] {
				continue
			}
			seen[key] = true
			size := len(mustJSON(c))
			if len(merged) >= 240 || bytes+size > 120*1024 || docBytes[c.DocumentID]+size > 80*1024 {
				limited = true
				continue
			}
			merged = append(merged, c)
			bytes += size
			docBytes[c.DocumentID] += size
		}
	}
	for _, ev := range d.Events {
		if ev.Type != "workflow.local_evidence_requested" {
			continue
		}
		var p struct {
			Output      json.RawMessage `json:"previousSearchOutput"`
			Hash        string          `json:"previousSearchHash"`
			MaterialKey string          `json:"materialKey"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil || len(p.Output) == 0 || p.MaterialKey != localEvidenceMaterialKey(d) {
			continue
		}
		if hashJSON(p.Output) != p.Hash {
			return result, fmt.Errorf("previous local evidence snapshot changed")
		}
		var prior struct {
			Citations []tool.CitationRef `json:"citations"`
		}
		if err := json.Unmarshal(p.Output, &prior); err != nil {
			return result, err
		}
		add(prior.Citations)
	}
	if len(merged) == 0 {
		return result, nil
	}
	add(result.Citations)
	result.Citations = merged
	result.Truncated = result.Truncated || limited
	v := decodeObject(result.Structured)
	v["totalMatches"] = len(merged)
	coverage := []map[string]any{}
	_ = json.Unmarshal(mustJSON(v["documentCoverage"]), &coverage)
	for _, c := range coverage {
		count := 0
		for _, ref := range merged {
			if ref.DocumentID == c["documentId"] {
				count++
			}
		}
		c["excerptCount"] = count
		if limited {
			c["selectionLimited"] = true
		}
	}
	v["documentCoverage"] = coverage
	result.Structured = mustJSON(v)
	result.Text = fmt.Sprintf("Retained %d exact-source excerpts across bounded local follow-ups; not full-text reading.", len(merged))
	return result, nil
}
