package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
)

const literatureScreeningVersion = "dynamic-candidate-screening-v11"
const literatureCheckpointEvent = "workflow.literature_checkpoint"
const literatureBatchSize = 20
const literatureMaxSupplementRounds = 3
const literatureMaxBatches = 80

const literatureScreeningInstruction = "宿主执行分批筛选与最多三轮自动补检索。_literature.phase=batch 时，只逐项评估当前批次，不对全课题下证据不足结论；把待核验的人群、对照、研究类型与结果记入每项 reason，供后续跨批次综合。phase=coverage 时，candidates 是所有批次保留的候选，priorAssessments 是此前逐项判断，excludedCount 是已排除数量；根据这些材料评估覆盖，按研究相关性推荐材料，不得因为程序容量、批次大小或导入限制要求收窄研究范围。不能凭题名或摘要虚构全文结果。来源失败或 pending>0 代表检索未充分完成，不是学术证据不足。不足时优先生成针对真实缺口的新 supplementalQueries，宿主会自动执行，不要求用户搜索或确认补检索。每条查询建议少于200字符；不得把年份列表当作关键词替代数据库日期过滤。已有检索充分但需要改变研究范围则 recommendation=narrow_scope，不得自行放宽冻结标准。"

func literatureScreeningSchema() json.RawMessage {
	return hierarchySchema()
}

type LiteratureCandidateReader interface {
	LiteratureCandidates(context.Context, string, []string) ([]research.Candidate, error)
}
type literatureContinuationRepository interface {
	QueueLiteratureContinuation(context.Context, string, string, string, int, time.Time, RuntimeEvent) error
}

func (s *RuntimeService) SetLiteratureCandidateReader(reader LiteratureCandidateReader) {
	s.literature = reader
}

type literatureCheckpoint struct {
	CoverageExecution   string              `json:"coverageExecution,omitempty"`
	CoverageKey         string              `json:"coverageKey,omitempty"`
	SynthesisGroups     [][]string          `json:"synthesisGroups,omitempty"`
	RecheckedSources    map[string]string   `json:"recheckedSources,omitempty"`
	RecheckRequests     []literatureRecheck `json:"recheckRequests,omitempty"`
	ReadbackExecution   string              `json:"readbackExecution,omitempty"`
	SynthesisExecutions []string            `json:"synthesisExecutions,omitempty"`
	RecheckIDs          []string            `json:"recheckIds,omitempty"`
	RecheckRounds       int                 `json:"recheckRounds,omitempty"`
	PageFailures        map[string]int      `json:"pageFailures,omitempty"`
	Pages               []literaturePage    `json:"pages,omitempty"`
	PageLedger          map[string]int      `json:"pageLedger,omitempty"`
	PageRequests        int                 `json:"pageRequests"`
	NextSources         []string            `json:"nextSources,omitempty"`
	FailedSearches      map[string]bool     `json:"failedSearches,omitempty"`
	NextOffset          int                 `json:"nextOffset"`
	PageQueries         []string            `json:"pageQueries"`
	PageOffset          int                 `json:"pageOffset"`
	SourceLimits        bool                `json:"sourceLimits"`
	QueryIDs            []string            `json:"queryIds"`
	Queries             []string            `json:"queries"`
	NextQueries         []string            `json:"nextQueries"`
	Executions          []string            `json:"executions"`
	Phase               string              `json:"phase"`
	Round               int                 `json:"round"`
	Batches             int                 `json:"batches"`
	BeforeCount         int                 `json:"beforeCount"`
	NoGrowth            int                 `json:"noGrowth"`
	SourceFailures      bool                `json:"sourceFailures"`
}
type literatureCandidate struct {
	Venue          string               `json:"venue,omitempty"`
	Identifiers    research.Identifiers `json:"identifiers,omitempty"`
	SourceSHA256   string               `json:"sourceSha256,omitempty"`
	Years          []int                `json:"years,omitempty"`
	MetadataStatus string               `json:"metadataStatus,omitempty"`
	ID             string               `json:"id"`
	Title          string               `json:"title"`
	Authors        []research.Author    `json:"authors"`
	Year           int                  `json:"year"`
	DOI            string               `json:"doi"`
	WorkType       string               `json:"workType"`
	Abstract       string               `json:"abstract,omitempty"`
	SourceIDs      []string             `json:"sourceIds"`
}
type literatureAssessment struct {
	ImportAction string `json:"importAction,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	CandidateID  string `json:"candidateId"`
	Decision     string `json:"decision"`
	Relevance    string `json:"relevance"`
	Reason       string `json:"reason"`
}
type literatureInput struct {
	Triage              bool                   `json:"triage,omitempty"`
	CoverageKey         string                 `json:"coverageKey,omitempty"`
	AutomaticExcluded   int                    `json:"automaticExcluded"`
	SynthesisKey        string                 `json:"synthesisKey,omitempty"`
	Level               int                    `json:"level,omitempty"`
	Readback            bool                   `json:"readback,omitempty"`
	AutomaticExclusions []literatureAssessment `json:"automaticExclusions,omitempty"`
	Phase               string                 `json:"phase"`
	State               literatureCheckpoint   `json:"state"`
	Fingerprints        map[string]string      `json:"fingerprints"`
	Total               int                    `json:"total"`
	Pending             int                    `json:"pending"`
}
type literatureScreening struct {
	Rechecks                []literatureRecheck    `json:"rechecks,omitempty"`
	Phase                   string                 `json:"phase"`
	EvidenceNotes           []literatureNote       `json:"evidenceNotes,omitempty"`
	Findings                []literatureFinding    `json:"findings,omitempty"`
	Uncertainties           []literatureFinding    `json:"uncertainties,omitempty"`
	RecheckCandidateIDs     []string               `json:"recheckCandidateIds,omitempty"`
	Summary                 string                 `json:"summary"`
	RecommendedCandidateIDs []string               `json:"recommendedCandidateIds"`
	CandidateAssessments    []literatureAssessment `json:"candidateAssessments"`
	Coverage                struct {
		Strength   string `json:"strength"`
		Sufficient bool   `json:"sufficientForClaimedScope"`
	} `json:"coverage"`
	SupplementalQueries []string `json:"supplementalQueries"`
	Recommendation      string   `json:"recommendation"`
}

func literatureState(detail RunDetail) literatureCheckpoint {
	state := literatureCheckpoint{Phase: "batch"}
	discoveryID := ""
	discoveryOrdinal := -1
	for _, step := range detail.Steps {
		if step.NodeID == "literature_discovery" {
			discoveryID = step.ID
			discoveryOrdinal = step.Ordinal
		}
	}
	for _, event := range detail.Events {
		if event.Type == "workflow.user_revision_queued" {
			var payload struct {
				StartOrdinal int `json:"startOrdinal"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil && payload.StartOrdinal <= discoveryOrdinal {
				state = literatureCheckpoint{Phase: "batch"}
			}
		}
		if event.Type == "workflow.upstream_revision_queued" || event.Type == "workflow.review_revision_queued" {
			var payload map[string]any
			_ = json.Unmarshal(event.Payload, &payload)
			if payload["stepId"] == discoveryID || payload["producerStepId"] == discoveryID {
				state = literatureCheckpoint{Phase: "batch"}
			}
		}
		if event.Type == literatureCheckpointEvent {
			var value struct {
				State       literatureCheckpoint `json:"state"`
				DiscoveryID string               `json:"discoveryId"`
			}
			if json.Unmarshal(event.Payload, &value) == nil && value.DiscoveryID == discoveryID {
				state = value.State
			}
		}
	}
	return state
}

func appendUnique(values []string, additions ...string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value != "" && !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	return values
}

func literatureDiscovery(detail RunDetail) (Step, []string, []string, bool) {
	for _, step := range detail.Steps {
		if step.NodeID != "literature_discovery" {
			continue
		}
		var output struct {
			Structured struct {
				QueryIDs []string `json:"queryIds"`
				Queries  []string `json:"queries"`
				Partial  bool     `json:"partial"`
			} `json:"structured"`
		}
		_ = json.Unmarshal(step.Output, &output)
		return step, output.Structured.QueryIDs, output.Structured.Queries, output.Structured.Partial
	}
	return Step{}, nil, nil, false
}

func literatureCandidates(values []research.Candidate) []literatureCandidate {
	result := make([]literatureCandidate, 0, len(values))
	for _, c := range values {
		w := c.Preferred
		sources := []string{}
		years := []int{w.Year}
		years = append(years, w.PublicationYears...)
		for _, record := range c.Records {
			years = append(years, record.Work.Year)
			years = append(years, record.Work.PublicationYears...)
			sources = appendUnique(sources, record.Work.SourceID)
			// A more complete publisher record must not hide an available abstract.
			if w.Abstract == "" && record.Work.Abstract != "" {
				w.Abstract = record.Work.Abstract
			}
		}
		sort.Ints(years)
		unique := years[:0]
		for _, year := range years {
			if len(unique) == 0 || unique[len(unique)-1] != year {
				unique = append(unique, year)
			}
		}
		years = unique
		sort.Strings(sources)
		result = append(result, literatureCandidate{ID: c.ID, Title: w.Title, Authors: w.Authors, Year: w.Year, DOI: w.Identifiers.DOI, Venue: w.Venue, Identifiers: w.Identifiers, WorkType: w.WorkType, Abstract: w.Abstract, SourceIDs: sources, MetadataStatus: w.MetadataFetchStatus, Years: years})
	}
	return result
}

func literaturePrior(detail RunDetail, state literatureCheckpoint) (map[string]literatureAssessment, map[string]string) {
	assessments := map[string]literatureAssessment{}
	fingerprints := map[string]string{}
	byID := map[string]AIExecution{}
	for _, e := range detail.AIExecutions {
		byID[e.ID] = e
	}
	for _, id := range state.Executions {
		e, ok := byID[id]
		if !ok || e.Status != "completed" {
			continue
		}
		var output literatureScreening
		// Input is in the immutable stage prompt; use the checkpoint's per-batch
		// fingerprints from its event, rather than interpreting prompt text.
		if json.Unmarshal(e.Output, &output) != nil {
			continue
		}
		for _, event := range detail.Events {
			var payload struct {
				ExecutionID  string            `json:"executionId"`
				Fingerprints map[string]string `json:"fingerprints"`
			}
			if event.Type != literatureCheckpointEvent || json.Unmarshal(event.Payload, &payload) != nil || payload.ExecutionID != id {
				continue
			}
			for _, a := range output.CandidateAssessments {
				if f := payload.Fingerprints[a.CandidateID]; f != "" {
					assessments[a.CandidateID] = a
					fingerprints[a.CandidateID] = f
				}
			}
		}
	}
	return assessments, fingerprints
}

func (s *RuntimeService) prepareLiteratureInput(ctx context.Context, detail RunDetail, node CompiledNode, rawInput json.RawMessage) (json.RawMessage, error) {
	enabled := false
	for _, n := range detail.Run.Compilation.Nodes {
		if n.ID == "candidate_screening" && n.PromptVersion == literatureScreeningVersion {
			enabled = true
		}
	}
	if !enabled {
		return rawInput, nil
	}
	state := literatureState(detail)
	arguments := decodeObject(rawInput)
	if node.ID == "literature_discovery" {
		arguments["publicationYears"] = frozenLiteratureYears(detail)
		arguments["limit"] = 20
		arguments["offset"] = 0
		if planned, ok := arguments["queries"].([]any); ok && len(planned) > 0 {
			if len(planned) > 2 {
				return nil, fmt.Errorf("literature plan requires at most two complete queries")
			}
			arguments["query"] = planned[0]
			arguments["queries"] = planned[1:]
		}
	}
	if node.ID == "literature_discovery" && state.Phase == "search" {
		if len(state.NextQueries) == 0 {
			return nil, fmt.Errorf("literature continuation has no query")
		}
		arguments["query"] = state.NextQueries[0]
		arguments["queries"] = state.NextQueries[1:]
		arguments["limit"] = 20
		arguments["offset"] = state.NextOffset
		if len(state.NextSources) > 0 {
			arguments["sourceIds"] = state.NextSources
		} else {
			delete(arguments, "sourceIds")
		}
		return json.Marshal(arguments)
	}
	if node.ID != "candidate_screening" && node.ID != "candidate_review" {
		return json.Marshal(arguments)
	}
	if node.ID == "candidate_review" {
		for _, step := range detail.Steps {
			if step.NodeID == "candidate_screening" && step.Status == StepCompleted {
				var output struct {
					Candidates []literatureCandidate `json:"candidates"`
				}
				if json.Unmarshal(step.Output, &output) != nil || output.Candidates == nil {
					return nil, fmt.Errorf("frozen literature selection snapshot is missing")
				}
				arguments["candidates"] = output.Candidates
				encoded, err := json.Marshal(arguments)
				if err == nil && len(encoded) > 1024*1024 {
					return nil, fmt.Errorf("literature selection exceeds the persisted input budget")
				}
				return encoded, err
			}
		}
		return nil, fmt.Errorf("literature screening is not completed")
	}
	if s.literature == nil {
		return nil, fmt.Errorf("literature candidate reader is not configured")
	}
	discovery, queryIDs, queries, partial := literatureDiscovery(detail)
	if discovery.Status != StepCompleted {
		return nil, fmt.Errorf("literature discovery is not completed")
	}
	newSearch := false
	for _, id := range queryIDs {
		found := false
		for _, old := range state.QueryIDs {
			found = found || old == id
		}
		newSearch = newSearch || !found
	}
	state.QueryIDs = appendUnique(state.QueryIDs, queryIDs...)
	state.Queries = appendUnique(state.Queries, queries...)
	if state.FailedSearches == nil {
		state.FailedSearches = map[string]bool{}
	}
	var discoveryOutput struct {
		Structured struct {
			Sources []research.SourceSearch `json:"sources"`
		} `json:"structured"`
	}
	_ = json.Unmarshal(discovery.Output, &discoveryOutput)
	// A full first page is the configured retrieval boundary, not a paging request.
	state.Pages = nil
	state.PageLedger = nil
	state.SourceLimits = false
	lastOffset := 0
	for _, source := range discoveryOutput.Structured.Sources {
		state.SourceLimits = state.SourceLimits || source.LimitReached
		lastOffset = max(lastOffset, source.Offset)
		key := fmt.Sprintf("%s:%d:%s", source.SourceID, source.Offset, source.EffectiveQuery)
		if source.Status == research.SearchFailed {
			state.FailedSearches[key] = true
		} else {
			delete(state.FailedSearches, key)
		}
	}
	state.SourceFailures = len(state.FailedSearches) > 0 || partial
	if newSearch && state.SourceLimits {
		state.PageQueries = append([]string(nil), queries...)
		state.PageOffset = lastOffset + 20
	}
	if newSearch && !state.SourceLimits && lastOffset > 0 {
		state.PageQueries = nil
	}
	values, err := s.literature.LiteratureCandidates(ctx, detail.Run.ProjectID, state.QueryIDs)
	if err != nil {
		return nil, err
	}
	candidates := literatureCandidates(values)
	if state.Phase == "search" {
		if newSearch && len(candidates) <= state.BeforeCount {
			state.NoGrowth++
		} else if len(candidates) > state.BeforeCount {
			state.NoGrowth = 0
		}
		state.Phase = "batch"
	}
	prior, fingerprints := literaturePrior(detail, state)
	for _, id := range state.RecheckIDs {
		delete(fingerprints, id)
	}
	pending := []literatureCandidate{}
	fresh := map[string]string{}
	for _, c := range candidates {
		encoded, _ := json.Marshal(c)
		fingerprint := hashJSON(encoded)
		if fingerprints[c.ID] != fingerprint {
			pending = append(pending, c)
			fresh[c.ID] = fingerprint
		}
	}
	info := literatureInput{Phase: "batch", State: state, Fingerprints: map[string]string{}, Total: len(candidates), Pending: len(pending)}
	filtered := pending[:0]
	readbackIDs := map[string]bool{}
	for _, id := range state.RecheckIDs {
		readbackIDs[id] = true
	}
	for _, c := range pending {
		if reason := automaticLiteratureExclusion(c, frozenLiteratureYears(detail)); reason != "" && !readbackIDs[c.ID] {
			info.AutomaticExcluded++
			prior[c.ID] = literatureAssessment{CandidateID: c.ID, Decision: "exclude", Relevance: "low", Reason: reason}
		} else {
			filtered = append(filtered, c)
		}
	}
	pending = filtered
	if len(state.RecheckIDs) > 0 {
		wanted := map[string]bool{}
		for _, id := range state.RecheckIDs {
			wanted[id] = true
		}
		readback := []literatureCandidate{}
		for _, c := range pending {
			if wanted[c.ID] {
				readback = append(readback, c)
			}
		}
		if len(readback) > 0 {
			pending = readback
			info.Readback = true
		}
	}
	info.Pending = len(pending)
	info.Triage = len(pending) > 0
	batch := []literatureCandidate{}
	bytes := 0
	if len(pending) > 0 && state.Batches < literatureMaxBatches {
		for _, c := range pending {
			encoded, _ := json.Marshal(c)
			if len(batch) >= literatureBatchSize || bytes+len(encoded) > 100*1024 {
				break
			}
			batch = append(batch, c)
			info.Fingerprints[c.ID] = fresh[c.ID]
			bytes += len(encoded)
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("a literature record exceeds the screening input budget; no evidence conclusion was made")
		}
		remaining := []string{}
		for _, id := range state.RecheckIDs {
			if info.Fingerprints[id] == "" {
				remaining = append(remaining, id)
			}
		}
		info.State.RecheckIDs = remaining
	} else {
		if len(pending) == 0 {
			info.Phase = "selection"
			arguments["_literature"] = info
			return boundedLiteratureInput(arguments)
		}
		return prepareLiteratureSynthesis(detail, candidates, prior, fingerprints, arguments, info)
	}
	arguments["candidates"] = batch
	arguments["_literature"] = info
	arguments["partialDiscovery"] = state.SourceFailures
	arguments["publicationYears"] = frozenLiteratureYears(detail)
	if info.Readback {
		arguments["readbackReason"] = "全局综合请求核对该候选的原始材料；本次逐项判断将替换先前初筛，并重新生成受影响综合。检查冲突、误排和同一试验重复报告。"
		arguments["readbackQuestions"] = state.RecheckRequests
		for _, e := range detail.AIExecutions {
			if e.ID == state.ReadbackExecution {
				result := decodeObject(e.Output)
				arguments["readbackContext"] = map[string]any{"summary": result["summary"], "findings": result["findings"], "uncertainties": result["uncertainties"]}
			}
		}
	}
	return boundedLiteratureInput(arguments)
}

func boundedLiteratureInput(arguments map[string]any) (json.RawMessage, error) {
	result, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	if len(result) > 220*1024 {
		return nil, fmt.Errorf("literature screening context budget exhausted; candidates remain saved, search is incomplete")
	}
	return result, nil
}

func (s *RuntimeService) completeLiteratureScreening(ctx context.Context, detail RunDetail, step Step, node CompiledNode, execution AIExecution, structured json.RawMessage) error {
	var input struct {
		Literature literatureInput `json:"_literature"`
	}
	var screening literatureScreening
	if json.Unmarshal(step.Input, &input) != nil || json.Unmarshal(structured, &screening) != nil {
		return fmt.Errorf("invalid literature checkpoint")
	}
	info := input.Literature
	state := info.State
	discovery, _, _, _ := literatureDiscovery(detail)
	continueAt := ""
	continuing := false
	if info.Phase == "batch" {
		state.Executions = appendUnique(state.Executions, execution.ID)
		state.Batches++
		state.Phase = "batch"
		if info.Readback {
			if state.RecheckedSources == nil {
				state.RecheckedSources = map[string]string{}
			}
			for id, fingerprint := range info.Fingerprints {
				state.RecheckedSources[id] = fingerprint
			}
		}
		continuing = true
	} else if info.Phase == "synthesis" {
		state.SynthesisExecutions = appendUnique(state.SynthesisExecutions, execution.ID)
		state.Phase = "synthesis"
		continuing = true
	} else if info.Phase == "coverage" {
		state.CoverageExecution = execution.ID
		state.CoverageKey = info.CoverageKey
		if len(screening.RecheckCandidateIDs) > 0 && state.RecheckRounds < 2 {
			state.RecheckIDs = append([]string(nil), screening.RecheckCandidateIDs...)
			state.RecheckRounds++
			state.ReadbackExecution = execution.ID
			state.RecheckRequests = screening.Rechecks
			state.Phase = "batch"
			continuing = true
		}
	} else {
		return fmt.Errorf("unknown literature screening phase")
	}
	if continuing {
		repository, ok := s.repository.(literatureContinuationRepository)
		if !ok {
			return fmt.Errorf("literature continuation repository is not configured")
		}
		event, err := s.event(detail.Run.ID, literatureCheckpointEvent, map[string]any{"stepId": step.ID, "discoveryId": discovery.ID, "executionId": execution.ID, "fingerprints": info.Fingerprints, "state": state, "synthesisKey": info.SynthesisKey}, s.now())
		if err != nil {
			return err
		}
		return repository.QueueLiteratureContinuation(ctx, detail.Run.ID, step.ID, continueAt, step.Attempt, s.now(), event)
	}
	status := "assessed"
	stopReason := "coverage_assessed"
	if state.SourceFailures {
		status = "source_blocked"
		stopReason = "source_failures"
	} else if execution.ID == "" && info.Pending == 0 {
		stopReason = "initial_relevance_screening_complete"
	} else if literaturePagesIncomplete(state) || info.Pending > 0 || ((!screening.Coverage.Sufficient || screening.Recommendation == "expand_search") && screening.Recommendation != "narrow_scope") {
		status = "budget_exhausted"
		stopReason = "supplement_budget"
		if info.Pending > 0 {
			stopReason = "screening_budget"
		}
	} else if screening.Recommendation == "narrow_scope" {
		stopReason = "scope_change_requires_confirmation"
	}
	if len(screening.RecheckCandidateIDs) > 0 {
		status = "budget_exhausted"
		stopReason = "recheck_budget"
	}
	analysis := decodeObject(structured)
	if err := s.finalizeLiteratureEvidence(ctx, detail, info, &analysis); err != nil {
		return err
	}
	analysis["retrieval"] = map[string]any{"status": status, "stopReason": stopReason, "supplementRounds": state.Round, "screenedBatches": state.Batches, "candidateCount": info.Total, "pendingCount": info.Pending}
	if status != "assessed" {
		coverage := decodeObject(mustJSON(analysis["coverage"]))
		coverage["sufficientForClaimedScope"] = false
		coverage["strength"] = "limited"
		analysis["coverage"] = coverage
		message := literatureBudgetMessage(state)
		if stopReason == "no_new_candidates" {
			message = "连续两轮补检索未增加新候选，已停止重复搜索；当前材料仍未覆盖研究范围，不能据此推断整个领域没有相关研究。"
		}
		if status == "source_blocked" {
			message = "部分文献来源未成功返回；已完成可用来源的首页检索，来源访问限制不代表该课题缺少研究。"
		}
		analysis["summary"] = message + "\n" + screening.Summary
	}
	selection := analysis["selectionCandidates"]
	delete(analysis, "selectionCandidates")
	manifest := analysis["evidenceManifest"]
	delete(analysis, "evidenceManifest")
	output, _ := json.Marshal(map[string]any{"analysis": analysis, "text": execution.OutputText, "candidates": selection, "evidenceManifest": manifest})
	next := step.Ordinal + 1
	final, outputs, err := finalOutputs(detail.Run, detail.Steps, step.ID, output, next)
	if err != nil {
		return err
	}
	event, err := s.event(detail.Run.ID, "workflow.ai_completed", map[string]any{"stepId": step.ID, "executionId": execution.ID, "retrievalStatus": status}, s.now())
	if err != nil {
		return err
	}
	return s.repository.CompleteStep(ctx, detail.Run.ID, step.ID, output, next, final, outputs, s.now(), event)
}

func mustJSON(value any) json.RawMessage { result, _ := json.Marshal(value); return result }
