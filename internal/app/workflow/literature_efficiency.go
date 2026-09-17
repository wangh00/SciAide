package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type literatureRecheck struct {
	CandidateID string `json:"candidateId"`
	Question    string `json:"question"`
}

func literatureCoverageInput(args map[string]any, info literatureInput) (json.RawMessage, error) {
	// Host queue progress does not change the scientific evidence. Source
	// availability and unresolved rereads do, so they remain in the cache key.
	material := make(map[string]any, len(args))
	for key, value := range args {
		if key != "_literature" {
			material[key] = value
		}
	}
	material["coverageState"] = map[string]any{"pending": info.Pending, "recheckRounds": info.State.RecheckRounds, "recheckedSources": info.State.RecheckedSources, "fingerprints": info.Fingerprints, "pagesIncomplete": literaturePagesIncomplete(info.State), "sourceFailures": info.State.SourceFailures}
	info.CoverageKey = hashJSON(mustJSON(material))
	args["_literature"] = info
	return boundedLiteratureInput(args)
}

func (s *RuntimeService) reuseLiteratureCoverage(ctx context.Context, detail RunDetail, step Step, node CompiledNode) (bool, error) {
	if node.ID != "candidate_screening" || node.PromptVersion != literatureScreeningVersion {
		return false, nil
	}
	var in struct {
		Literature literatureInput `json:"_literature"`
	}
	if json.Unmarshal(step.Input, &in) != nil || in.Literature.Phase != "coverage" {
		return false, nil
	}
	info := in.Literature
	if info.CoverageKey == "" || info.CoverageKey != info.State.CoverageKey {
		return false, nil
	}
	for _, execution := range detail.AIExecutions {
		if execution.ID != info.State.CoverageExecution || execution.Status != "completed" || execution.OutputSHA256 == "" || execution.OutputSHA256 != hashJSON(execution.Output) {
			continue
		}
		if step.InputSHA256 == "" || hashJSON(step.Input) != step.InputSHA256 {
			return true, fmt.Errorf("literature coverage input snapshot changed")
		}
		if code, err := s.validateRunSnapshots(ctx, detail); err != nil {
			return true, s.failBlockedStep(ctx, detail, code, err)
		}
		node = literaturePhaseNode(node, step.Input)
		if (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, execution.Output) != nil || validateWorkflowAIStageOutput(node, execution.Output, step.Input) != nil {
			return false, nil
		}
		event, err := s.event(detail.Run.ID, "workflow.literature_coverage_reused", map[string]any{"stepId": step.ID, "executionId": execution.ID, "coverageKey": info.CoverageKey}, s.now())
		if err != nil {
			return true, err
		}
		if err := s.repository.RecordEvent(ctx, event); err != nil {
			return true, err
		}
		return true, s.completeLiteratureScreening(ctx, detail, step, node, execution, execution.Output)
	}
	return false, nil
}

func (s *RuntimeService) continueLiteraturePages(ctx context.Context, detail RunDetail, step Step, node CompiledNode) (bool, error) {
	if node.ID != "candidate_screening" || node.PromptVersion != literatureScreeningVersion {
		return false, nil
	}
	var in struct {
		Literature literatureInput `json:"_literature"`
	}
	if json.Unmarshal(step.Input, &in) != nil || in.Literature.Phase != "search" {
		return false, nil
	}
	if step.InputSHA256 == "" || hashJSON(step.Input) != step.InputSHA256 {
		return true, fmt.Errorf("literature paging input snapshot changed")
	}
	if code, err := s.validateRunSnapshots(ctx, detail); err != nil {
		return true, s.failBlockedStep(ctx, detail, code, err)
	}
	repo, ok := s.repository.(literatureContinuationRepository)
	if !ok {
		return true, fmt.Errorf("literature continuation repository is not configured")
	}
	discovery, _, _, _ := literatureDiscovery(detail)
	event, err := s.event(detail.Run.ID, literatureCheckpointEvent, map[string]any{
		"stepId": step.ID, "discoveryId": discovery.ID, "state": in.Literature.State, "reason": "source_paging_without_ai",
	}, s.now())
	if err != nil {
		return true, err
	}
	return true, repo.QueueLiteratureContinuation(ctx, detail.Run.ID, step.ID, discovery.ID, step.Attempt, s.now(), event)
}

// Group membership is frozen in the checkpoint. Appended candidates cannot
// shift every subsequent group; an oversized changed group alone is split.
func stableLiteratureGroups(records []literatureEvidenceRecord, previous [][]string) ([][]literatureEvidenceRecord, [][]string) {
	byID := make(map[string]literatureEvidenceRecord, len(records))
	for _, record := range records {
		byID[record.CandidateID] = record
	}
	groups := [][]literatureEvidenceRecord{}
	membership := [][]string{}
	seen := map[string]bool{}
	pack := func(values []literatureEvidenceRecord) {
		for start := 0; start < len(values); {
			end, size := start, 0
			ids := []string{}
			for end < len(values) && end-start < 50 {
				n := len(mustJSON(values[end]))
				if end > start && size+n > 48*1024 {
					break
				}
				size += n
				ids = append(ids, values[end].CandidateID)
				end++
			}
			groups = append(groups, values[start:end])
			membership = append(membership, ids)
			start = end
		}
	}
	for _, ids := range previous {
		values := []literatureEvidenceRecord{}
		for _, id := range ids {
			if record, ok := byID[id]; ok && !seen[id] {
				values = append(values, record)
				seen[id] = true
			}
		}
		pack(values)
	}
	added := []literatureEvidenceRecord{}
	for _, record := range records {
		if !seen[record.CandidateID] {
			added = append(added, record)
			seen[record.CandidateID] = true
		}
	}
	pack(added)
	return groups, membership
}

func validateLiteratureRechecks(result literatureScreening, info literatureInput, offered map[string]bool) error {
	if len(result.Rechecks) != len(result.RecheckCandidateIDs) {
		return fmt.Errorf("every source recheck needs exactly one candidateId/question in rechecks")
	}
	wanted := map[string]bool{}
	for _, id := range result.RecheckCandidateIDs {
		wanted[id] = true
	}
	for _, request := range result.Rechecks {
		if !wanted[request.CandidateID] || !offered[request.CandidateID] || strings.TrimSpace(request.Question) == "" {
			return fmt.Errorf("source recheck must identify an offered candidate and a concrete question")
		}
		delete(wanted, request.CandidateID)
		if info.State.RecheckRounds >= 2 {
			return fmt.Errorf("source reread budget exhausted; retain unresolved questions in uncertainties rather than request another reread")
		}
		if info.State.RecheckedSources[request.CandidateID] != "" && info.State.RecheckedSources[request.CandidateID] == info.Fingerprints[request.CandidateID] {
			return fmt.Errorf("candidate %s has already been reread with identical source content; record remaining full-text or identity limitations in uncertainties", request.CandidateID)
		}
	}
	return nil
}

// Runtime paging ledgers and hashes stay in the frozen input/audit. The model
// needs only scientific materials and compact status, not opaque host state.
func literatureModelInput(input json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return input
	}
	var info literatureInput
	if json.Unmarshal(obj["_literature"], &info) != nil {
		return input
	}
	already := []string{}
	for id, fingerprint := range info.State.RecheckedSources {
		if fingerprint == info.Fingerprints[id] {
			already = append(already, id)
		}
	}
	obj["_literature"] = mustJSON(map[string]any{
		"phase": info.Phase, "triage": info.Triage, "total": info.Total, "pending": info.Pending, "readback": info.Readback, "level": info.Level,
		"automaticExcluded": info.AutomaticExcluded, "alreadyRecheckedCandidateIds": uniqueSortedStrings(already),
		"remainingRecheckRounds": max(0, 2-info.State.RecheckRounds),
		"retrieval":              map[string]any{"supplementRounds": info.State.Round, "sourceFailures": info.State.SourceFailures, "pagesIncomplete": literaturePagesIncomplete(info.State)},
	})
	if len(obj["candidates"]) > 0 {
		obj["candidates"] = projectLiteratureCandidateSegments(obj["candidates"])
	}
	return mustJSON(obj)
}
