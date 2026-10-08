package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangh00/SciAide/internal/app/citation"
	"github.com/wangh00/SciAide/internal/app/tool"
)

type indexedEvidencePage struct {
	Key                  string             `json:"materialKey"`
	Run                  string             `json:"chatRunId"`
	StepID               string             `json:"stepId"`
	ExecutionID          string             `json:"executionId"`
	Attempt              int                `json:"attempt"`
	Citations            []tool.CitationRef `json:"citations"`
	AcceptedOutputSHA256 string             `json:"acceptedOutputSha256,omitempty"`
}

func indexedEvidencePages(d RunDetail) []indexedEvidencePage {
	pages := []indexedEvidencePage{}
	for _, ev := range d.Events {
		payload := ev.Payload
		if ev.Type != "workflow.indexed_evidence_read" {
			if ev.Type != "workflow.ai_completed" && ev.Type != "workflow.completed" && ev.Type != "workflow.agent_stage_review_requested" && ev.Type != "workflow.selected_evidence_batch" && ev.Type != "workflow.local_evidence_requested" {
				continue
			}
			var completed struct {
				Evidence json.RawMessage `json:"indexedEvidence"`
			}
			if json.Unmarshal(payload, &completed) != nil || len(completed.Evidence) == 0 {
				continue
			}
			payload = completed.Evidence
		}
		var page indexedEvidencePage
		if json.Unmarshal(payload, &page) == nil && page.Key != "" && page.Key == localEvidenceMaterialKey(d) {
			pages = append(pages, page)
		}
	}
	return pages
}

func indexedEvidenceSnapshots(d RunDetail) []tool.CitationRef {
	refs := []tool.CitationRef{}
	seen := map[string]bool{}
	// Audit snapshots and model working-set markers have different identities.
	// A later reread may issue the same marker with a different complete source
	// snapshot (notably a legacy empty ID). Preserve each exact issued snapshot,
	// newest first, instead of silently throwing away a verified later source.
	pages := indexedEvidencePages(d)
	for i := len(pages) - 1; i >= 0; i-- {
		for _, ref := range pages[i].Citations {
			key := string(mustJSON(ref))
			if !seen[key] {
				refs = append(refs, ref)
				seen[key] = true
			}
		}
	}
	return refs
}

func bindIndexedEvidence(d RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	if node.ID != "evidence_screening" && node.ID != "evidence_extraction" {
		return input
	}
	refs := indexedEvidenceSnapshots(d)
	if len(refs) == 0 {
		return input
	}
	v := decodeObject(input)
	var old []tool.CitationRef
	_ = json.Unmarshal(mustJSON(v["candidates"]), &old)
	// The audit retains every intact chunk. A bounded working set prioritizes
	// new reads and already recommended evidence, rather than overflowing the
	// model input or truncating quotes into invalid evidence.
	priority := []tool.CitationRef{}
	selectionFromScreen := false
	pages := indexedEvidencePages(d)
	for i := len(pages) - 1; i >= 0; i-- {
		priority = append(priority, pages[i].Citations...)
	}
	if node.ID == "evidence_extraction" {
		// Selection offers exactly the evidence seen by the accepted synthesis.
		if screen := findStepByNode(d.Steps, "evidence_screening"); screen != nil {
			var offered struct {
				Candidates []tool.CitationRef `json:"candidates"`
			}
			if json.Unmarshal(screen.Input, &offered) == nil {
				priority = offered.Candidates
				selectionFromScreen = true
			}
			if accepted, ok := acceptedIndexedCandidates(d, *screen); ok {
				priority = accepted
			}
		}
	}
	byRef := map[string]tool.CitationRef{}
	for _, c := range old {
		byRef[c.Reference] = c
	}
	// Match the seed resolver's canonical preference for compact projections:
	// latest actual read first. Complete frozen candidates are never replaced.
	for i := len(refs) - 1; i >= 0; i-- {
		byRef[refs[i].Reference] = refs[i]
	}
	pinned := []tool.CitationRef{}
	for _, e := range d.AIExecutions {
		if e.Status != "completed" {
			continue
		}
		var out struct {
			Recommended []string `json:"recommendedReferences"`
		}
		_ = json.Unmarshal(e.Output, &out)
		for _, r := range out.Recommended {
			if c, ok := byRef[r]; ok {
				pinned = append(pinned, c)
			}
		}
	}
	seen := map[string]bool{}
	kept := []tool.CitationRef{}
	bytes := 0
	docBytes := map[string]int{}
	working := append(append(append([]tool.CitationRef{}, priority...), pinned...), old...)
	working = append(working, refs...)
	if selectionFromScreen {
		// Offer the accepted synthesis working set only, not additional audit
		// chunks or candidates omitted by its model-input budget.
		working = priority
	}
	for _, c := range working {
		// Coverage input may contain compact projections. Recover intact
		// source identity before offering anything to citation selection.
		if full, ok := byRef[c.Reference]; ok && c.IndexVersionID == "" {
			c = full
		}
		if seen[c.Reference] {
			continue
		}
		seen[c.Reference] = true
		size := len(mustJSON(c))
		if !selectionFromScreen && (len(kept) >= 240 || bytes+size > 80*1024 || docBytes[c.DocumentID]+size > 64*1024) {
			continue
		}
		kept = append(kept, c)
		bytes += size
		docBytes[c.DocumentID] += size
	}
	v["candidates"] = kept
	readRefs := map[string]bool{}
	for _, ref := range refs {
		readRefs[ref.Reference] = true
	}
	v["indexedEvidenceWorkingSet"] = map[string]any{"readChunkCount": len(readRefs), "offeredChunkCount": len(kept), "omittedChunkCount": max(0, len(byRef)-len(kept)), "note": "完整核读记录保留在宿主审计；可引用当前候选及本轮成功核读工具实际签发的原文引用。未列出的旧块需从资源菜单重新核读，不能猜测引用；重读旧块不会自动重开综合阶段。"}
	ranges := []map[string]any{}
	for _, c := range kept {
		if readRefs[c.Reference] {
			ranges = append(ranges, indexedReadRange(c))
		}
	}
	v["indexedReadRanges"] = ranges
	return mustJSON(v)
}

// A new immutable input, not a splice into the model's scientific result.
func (s *RuntimeService) currentIndexedEvidence(ctx context.Context, d RunDetail, step Step, node CompiledNode, e AIExecution) ([]tool.CitationRef, error) {
	authorized := false
	for _, t := range node.AllowedTools {
		authorized = authorized || t.QualifiedName == "builtin.knowledge.read"
	}
	if !authorized || node.ID != "evidence_screening" || e.ChatRunID == "" || s.tools == nil || localEvidenceMaterialKey(d) == "" {
		return nil, nil
	}
	if e.WorkflowStepID != step.ID || e.Attempt != step.Attempt || e.InputSHA256 != step.InputSHA256 || hashJSON(step.Input) != step.InputSHA256 {
		return nil, fmt.Errorf("indexed read execution binding changed")
	}
	calls, err := s.tools.ListBySubject(ctx, tool.SubjectChatRun, e.ChatRunID)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, id := range selectedEvidenceAttachments(d) {
		allowed[id] = true
	}
	seen := map[string]bool{}
	refs := []tool.CitationRef{}
	for _, c := range calls {
		if c.RunID != e.ChatRunID || c.ToolName != "builtin.resource.open" || c.Status != tool.CallCompleted || c.Result == nil || c.Result.Status != tool.ResultSuccess {
			continue
		}
		var envelope struct {
			SourceTool string `json:"sourceTool"`
		}
		_ = json.Unmarshal(c.Result.Structured, &envelope)
		if envelope.SourceTool != "builtin.knowledge.read" {
			continue
		}
		for _, ref := range c.Result.Citations {
			if !allowed[ref.AttachmentID] || ref.ProjectID != d.Run.ProjectID {
				return nil, fmt.Errorf("indexed read left selected evidence scope")
			}
			issued, err := citation.ReissueKnowledgeRefs(e.ChatRunID, d.Run.ID, d.Run.ProjectID, []tool.CitationRef{ref})
			if err != nil {
				return nil, err
			}
			key := string(mustJSON(issued[0]))
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, issued[0])
		}
	}
	return refs, nil
}

// Progress is cumulative source content, not membership of the bounded model
// window or a reader's legacy/current ID representation.
func (s *RuntimeService) newIndexedEvidence(ctx context.Context, d RunDetail, step Step, node CompiledNode, e AIExecution) ([]tool.CitationRef, error) {
	reads, err := s.currentIndexedEvidence(ctx, d, step, node, e)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, ref := range indexedEvidenceSnapshots(d) {
		seen[ref.Reference] = true
	}
	added := []tool.CitationRef{}
	for _, ref := range reads {
		if !seen[ref.Reference] {
			added = append(added, ref)
			seen[ref.Reference] = true
		}
	}
	return added, nil
}

// Tool results already observed in this Chat are usable evidence, without
// restarting the scientific synthesis. Freeze the exact used source snapshots
// separately from the immutable request input; never truncate or invent quotes.
func indexedSubmissionInput(step Step, value json.RawMessage, reads, seeds []tool.CitationRef) (json.RawMessage, error) {
	if len(reads) == 0 {
		return step.Input, nil
	}
	used := map[string]bool{}
	for _, marker := range evidenceMarkerPattern.FindAllString(string(value), -1) {
		used[marker] = true
	}
	v := decodeObject(step.Input)
	var candidates []tool.CitationRef
	if err := json.Unmarshal(mustJSON(v["candidates"]), &candidates); err != nil {
		return nil, err
	}
	// Batched coverage uses compact request projections. Freeze the exact
	// source resolved for that request, not a compact copy later hydrated from
	// a newer read with the same marker but different snapshot fields.
	byRef := map[string]tool.CitationRef{}
	for _, ref := range seeds {
		byRef[ref.Reference] = ref
	}
	for i, ref := range candidates {
		if ref.IndexVersionID == "" {
			full, ok := byRef[ref.Reference]
			if !ok {
				return nil, fmt.Errorf("compact evidence candidate has no verified source")
			}
			candidates[i] = full
		}
	}
	seen := map[string]bool{}
	for _, ref := range candidates {
		seen[ref.Reference] = true
	}
	for _, ref := range reads {
		if used[ref.Reference] && !seen[ref.Reference] {
			candidates = append(candidates, ref)
			seen[ref.Reference] = true
		}
	}
	if len(candidates) > citation.MaxSeededKnowledgeCitations {
		kept := []tool.CitationRef{}
		for _, ref := range candidates {
			if used[ref.Reference] {
				kept = append(kept, ref)
			}
		}
		if len(kept) > citation.MaxSeededKnowledgeCitations {
			return nil, fmt.Errorf("submission uses too many distinct evidence chunks")
		}
		for _, ref := range candidates {
			if !used[ref.Reference] && len(kept) < citation.MaxSeededKnowledgeCitations {
				kept = append(kept, ref)
			}
		}
		candidates = kept
	}
	v["candidates"] = candidates
	var ranges []map[string]any
	_ = json.Unmarshal(mustJSON(v["indexedReadRanges"]), &ranges)
	seenRanges := map[string]bool{}
	for _, r := range ranges {
		seenRanges[string(mustJSON(r))] = true
	}
	for _, ref := range reads {
		r := indexedReadRange(ref)
		if !seenRanges[string(mustJSON(r))] {
			ranges = append(ranges, r)
			seenRanges[string(mustJSON(r))] = true
		}
	}
	v["indexedReadRanges"] = ranges
	return mustJSON(v), nil
}

func acceptedIndexedCandidates(d RunDetail, step Step) ([]tool.CitationRef, bool) {
	refs, _, ok := acceptedIndexedEvidence(d, step)
	return refs, ok
}

// The execution/event retain the original byte hash. Embedding RawMessage in
// a step output compacts whitespace and escapes HTML; compare that exact Go
// projection, not a hash of the embedded bytes or a float64 semantic decode.
func sameEmbeddedEvidenceOutput(left, right json.RawMessage) bool {
	l, le := json.Marshal(left)
	r, re := json.Marshal(right)
	return le == nil && re == nil && string(l) == string(r)
}

func acceptedIndexedEvidence(d RunDetail, step Step) ([]tool.CitationRef, string, bool) {
	var out struct {
		Candidates         []tool.CitationRef `json:"evidenceCandidates"`
		Analysis           json.RawMessage    `json:"analysis"`
		SourceOutputSHA256 string             `json:"evidenceSourceOutputSha256"`
	}
	if json.Unmarshal(step.Output, &out) != nil || out.Candidates == nil {
		return nil, "", false
	}
	sourceHash := out.SourceOutputSHA256
	if out.SourceOutputSHA256 != "" {
		// A user can revise the synthesis before confirming a manual AI stage.
		// Keep the original verified evidence window, while binding the adopted
		// scientific result to the host's atomic decision record.
		adopted := false
		for _, ev := range d.Events {
			if ev.Type != "workflow.human_decided" {
				continue
			}
			var record struct {
				StepID           string `json:"stepId"`
				Approved         bool   `json:"approved"`
				OutputSHA256     string `json:"outputSha256"`
				SourceSHA256     string `json:"evidenceSourceOutputSha256"`
				CandidatesSHA256 string `json:"evidenceCandidatesSha256"`
			}
			if json.Unmarshal(ev.Payload, &record) == nil && record.StepID == step.ID && record.Approved && record.OutputSHA256 == hashJSON(out.Analysis) && record.SourceSHA256 == out.SourceOutputSHA256 && record.CandidatesSHA256 == hashJSON(mustJSON(out.Candidates)) {
				adopted = true
			}
		}
		if !adopted {
			return nil, "", false
		}
		sourceHash = out.SourceOutputSHA256
	}
	for _, ev := range d.Events {
		if ev.Type != "workflow.ai_completed" && ev.Type != "workflow.completed" && ev.Type != "workflow.agent_stage_review_requested" {
			continue
		}
		var record struct {
			StepID           string `json:"stepId"`
			ExecutionID      string `json:"executionId"`
			OutputSHA256     string `json:"outputSha256"`
			CandidatesSHA256 string `json:"evidenceCandidatesSha256"`
		}
		if json.Unmarshal(ev.Payload, &record) != nil || record.StepID != step.ID || record.CandidatesSHA256 != hashJSON(mustJSON(out.Candidates)) || (sourceHash != "" && record.OutputSHA256 != sourceHash) {
			continue
		}
		for _, e := range d.AIExecutions {
			if e.ID == record.ExecutionID && e.WorkflowStepID == step.ID && e.Attempt == step.Attempt && e.InputSHA256 == step.InputSHA256 && hashJSON(step.Input) == step.InputSHA256 && e.Status == "completed" && e.OutputSHA256 == record.OutputSHA256 && hashJSON(e.Output) == e.OutputSHA256 {
				if sourceHash == "" && !sameEmbeddedEvidenceOutput(out.Analysis, e.Output) {
					continue
				}
				candidateStep := step
				candidateStep.Input = mustJSON(map[string]any{"candidates": out.Candidates})
				if _, err := workflowCandidateCitationSeeds(d.Run.Compilation, d.Steps, candidateStep, indexedEvidenceSnapshots(d)); err == nil {
					return out.Candidates, e.OutputSHA256, true
				}
			}
		}
	}
	return nil, "", false
}

func (s *RuntimeService) indexedAIStageSubmission(ctx context.Context, d RunDetail, step Step, node CompiledNode, e AIExecution, value json.RawMessage) (json.RawMessage, json.RawMessage, []tool.CitationRef, error) {
	reads, err := s.currentIndexedEvidence(ctx, d, step, node, e)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(reads) == 0 {
		return canonicalCitationSubmission(value, d, step, node, e.ChatRunID), step.Input, reads, nil
	}
	seeds, err := s.workflowAICitationSeeds(ctx, d, step)
	if err != nil {
		return nil, nil, nil, err
	}
	value = restoreWorkflowCitationMarkers(value, append(seeds, reads...), e.ChatRunID)
	input, err := indexedSubmissionInput(step, value, reads, seeds)
	if err != nil {
		return nil, nil, nil, err
	}
	value = canonicalSupportingQuotes(value, input)
	return value, input, reads, nil
}

func (s *RuntimeService) checkpointIndexedEvidence(ctx context.Context, d RunDetail, step Step, node CompiledNode, e AIExecution) (bool, error) {
	refs, err := s.newIndexedEvidence(ctx, d, step, node, e)
	if err != nil || len(refs) == 0 {
		return false, err
	}
	repo, ok := s.repository.(literatureContinuationRepository)
	if !ok {
		return false, fmt.Errorf("indexed evidence continuation unavailable")
	}
	// This execution collected new source evidence. Its terminal text remains
	// an audit draft, not an accepted scientific synthesis of the old input.
	if e.Status != "completed" {
		e.Status, e.Output, e.OutputSHA256 = "completed", raw(`{}`), hashJSON(raw(`{}`))
		e.UpdatedAt = s.now()
		e.CompletedAt = &e.UpdatedAt
		if err := s.repository.FinishAIExecution(ctx, e); err != nil {
			return false, err
		}
	}
	event, err := s.event(d.Run.ID, "workflow.indexed_evidence_read", map[string]any{"stepId": step.ID, "attempt": step.Attempt, "executionId": e.ID, "chatRunId": e.ChatRunID, "materialKey": localEvidenceMaterialKey(d), "citations": refs, "message": "已核读新的原文页章，正在按新增证据综合；保留原引用，不重复导入或联网。"}, s.now())
	if err != nil {
		return false, err
	}
	return true, repo.QueueLiteratureContinuation(ctx, d.Run.ID, step.ID, "", step.Attempt, s.now(), event)
}

func (s *RuntimeService) verifiedIndexedEvidence(ctx context.Context, d RunDetail) ([]tool.CitationRef, error) {
	refs := indexedEvidenceSnapshots(d)
	if len(refs) == 0 {
		return refs, nil
	}
	if s.tools == nil {
		return nil, fmt.Errorf("indexed evidence audit unavailable")
	}
	selected := map[string]bool{}
	for _, id := range selectedEvidenceAttachments(d) {
		selected[id] = true
	}
	for _, ref := range refs {
		if !selected[ref.AttachmentID] {
			return nil, fmt.Errorf("indexed evidence left current selected materials")
		}
	}
	available := []tool.CitationRef{}
	for _, v := range indexedEvidencePages(d) {
		step := findStep(d.Steps, v.StepID)
		if step == nil || step.NodeID != "evidence_screening" || v.Attempt < 1 || step.Attempt < v.Attempt {
			return nil, fmt.Errorf("indexed evidence event has no matching source stage")
		}
		authorized := false
		for _, t := range compilationNodeMap(d.Run.Compilation)[step.NodeID].AllowedTools {
			authorized = authorized || t.QualifiedName == "builtin.knowledge.read"
		}
		bound := false
		for _, e := range d.AIExecutions {
			if e.ID == v.ExecutionID && e.WorkflowStepID == v.StepID && e.ChatRunID == v.Run && e.Attempt == v.Attempt && e.Status == "completed" && e.InputSHA256 != "" && hashJSON(e.Output) == e.OutputSHA256 {
				bound = v.AcceptedOutputSHA256 == "" && rawJSONEqual(e.Output, raw(`{}`)) || v.AcceptedOutputSHA256 != "" && v.AcceptedOutputSHA256 == e.OutputSHA256 && !rawJSONEqual(e.Output, raw(`{}`))
			}
		}
		if !authorized || !bound {
			return nil, fmt.Errorf("indexed evidence source execution binding changed")
		}
		calls, err := s.tools.ListBySubject(ctx, tool.SubjectChatRun, v.Run)
		if err != nil {
			return nil, err
		}
		pageAvailable := []tool.CitationRef{}
		for _, c := range calls {
			if c.RunID != v.Run || c.Status != tool.CallCompleted || c.ToolName != "builtin.resource.open" || c.Result == nil || c.Result.Status != tool.ResultSuccess {
				continue
			}
			var env struct {
				SourceTool string `json:"sourceTool"`
			}
			_ = json.Unmarshal(c.Result.Structured, &env)
			if env.SourceTool != "builtin.knowledge.read" {
				continue
			}
			issued, err := citation.ReissueKnowledgeRefs(v.Run, d.Run.ID, d.Run.ProjectID, c.Result.Citations)
			if err != nil {
				return nil, err
			}
			available = append(available, issued...)
			pageAvailable = append(pageAvailable, issued...)
		}
		if err := validateCitationSubset(v.Citations, pageAvailable); err != nil {
			return nil, err
		}
	}
	if err := validateCitationSubset(refs, available); err != nil {
		return nil, err
	}
	return refs, nil
}

// Publication receives verified host projections of the original successful
// calls, never fabricated model sources. Original chat-run calls remain intact.
func (s *RuntimeService) IndexedEvidenceSources(ctx context.Context, projectID, runID string) ([]tool.Call, error) {
	d, err := s.repository.GetRun(ctx, projectID, runID)
	if err != nil {
		return nil, err
	}
	refs, err := s.verifiedIndexedEvidence(ctx, d)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, r := range refs {
		wanted[r.Reference] = true
	}
	result := []tool.Call{}
	seen := map[string]bool{}
	for _, v := range indexedEvidencePages(d) {
		calls, err := s.tools.ListBySubject(ctx, tool.SubjectChatRun, v.Run)
		if err != nil {
			return nil, err
		}
		for _, c := range calls {
			if seen[c.ID] || c.ID == "" || c.RunID != v.Run || c.ToolName != "builtin.resource.open" || c.Status != tool.CallCompleted || c.Result == nil || c.Result.Status != tool.ResultSuccess {
				continue
			}
			var env struct {
				Source string `json:"sourceTool"`
			}
			_ = json.Unmarshal(c.Result.Structured, &env)
			if env.Source != "builtin.knowledge.read" {
				continue
			}
			issued, err := citation.ReissueKnowledgeRefs(v.Run, runID, projectID, c.Result.Citations)
			if err != nil {
				return nil, err
			}
			kept := []tool.CitationRef{}
			for _, r := range issued {
				if wanted[r.Reference] {
					kept = append(kept, r)
				}
			}
			if len(kept) == 0 {
				continue
			}
			seen[c.ID] = true
			copyResult := *c.Result
			copyResult.Citations = kept
			c.Result = &copyResult
			result = append(result, c)
		}
	}
	return result, nil
}

func workflowCitationSeeds(d RunDetail, step Step) []tool.CitationRef {
	return workflowCitationSeedsFromSteps(d.Run.Compilation, d.Steps, step, indexedEvidenceSnapshots(d))
}

// Host observations, not an AI assertion of having read an entire page.
func indexedReadRanges(d RunDetail) []map[string]any {
	if screen := findStepByNode(d.Steps, "evidence_screening"); screen != nil {
		if _, sourceHash, ok := acceptedIndexedEvidence(d, *screen); ok {
			// A checkedScope assertion may use an observed range without citing
			// a positive finding from it. Retain all ranges actually checked in
			// the accepted Chat, not just the positively cited candidate window.
			ranges := []map[string]any{}
			var in struct {
				Ranges []map[string]any `json:"indexedReadRanges"`
			}
			_ = json.Unmarshal(screen.Input, &in)
			ranges = append(ranges, in.Ranges...)
			for _, page := range indexedEvidencePages(d) {
				if page.StepID == screen.ID && page.Attempt == screen.Attempt && page.AcceptedOutputSHA256 == sourceHash {
					for _, ref := range page.Citations {
						ranges = append(ranges, indexedReadRange(ref))
					}
				}
			}
			unique := []map[string]any{}
			seen := map[string]bool{}
			for _, r := range ranges {
				key := string(mustJSON(r))
				if !seen[key] {
					unique = append(unique, r)
					seen[key] = true
				}
			}
			return unique
		}
		var in struct {
			Ranges []map[string]any `json:"indexedReadRanges"`
		}
		if json.Unmarshal(screen.Input, &in) == nil && in.Ranges != nil {
			return in.Ranges
		}
	}
	reads := []map[string]any{}
	refs := indexedEvidenceSnapshots(d)
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref.Reference] {
			continue
		}
		seen[ref.Reference] = true
		reads = append(reads, indexedReadRange(ref))
		if len(reads) == 240 {
			break
		}
	}
	return reads
}

func indexedReadRange(ref tool.CitationRef) map[string]any {
	return map[string]any{"documentId": ref.DocumentID, "attachmentId": ref.AttachmentID, "locator": ref.Locator, "sourceStart": ref.SourceStart, "sourceEnd": ref.SourceEnd, "reference": ref.Reference}
}
