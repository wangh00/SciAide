package workflow

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/wangh00/SciAide/internal/app/research"
)

const recordAuditVersion = "query-candidate-audit-v1"

type researchRecordAudit struct {
	Version        string             `json:"version"`
	Status         string             `json:"status"`
	Queries        []queryRecordAudit `json:"queries"`
	Interpretation string             `json:"interpretation"`
}
type recordIdentitySummary struct {
	RetainedCandidateDetails      int    `json:"retainedCandidateDetails"`
	OmittedCandidateDetails       int    `json:"omittedCandidateDetails"`
	Version                       string `json:"version"`
	RecordedCandidates            int    `json:"recordedCandidates"`
	MissingCandidates             int    `json:"missingCandidates"`
	RoleEntries                   int    `json:"roleEntries"`
	RelationEntries               int    `json:"relationEntries"`
	IndependentStudyCountVerified bool   `json:"independentStudyCountVerified"`
}
type queryRecordAudit struct {
	Identity       *recordIdentitySummary `json:"identityHints,omitempty"`
	Grouping       *recordGroupingSummary `json:"groupingMethod,omitempty"`
	QueryID        string                 `json:"queryId"`
	SourceRecords  int                    `json:"sourceRecordCount"`
	Candidates     int                    `json:"candidateCount"`
	GroupedRecords int                    `json:"groupedExtraRecordCount"`
	Groups         []candidateRecordGroup `json:"groups"`
}
type candidateRecordGroup struct {
	RecordIdentity *research.RecordIdentity `json:"recordIdentity,omitempty"`
	CandidateID    string                   `json:"candidateId"`
	Records        []candidateAuditRecord   `json:"records"`
}
type candidateAuditRecord struct {
	ID             string                    `json:"recordId"`
	Source         string                    `json:"source"`
	SourceRecordID string                    `json:"sourceRecordId"`
	Aliases        []research.CandidateAlias `json:"aliases"`
	Grouping       *research.RecordGrouping  `json:"grouping,omitempty"`
}

type recordGroupingSummary struct {
	Version  string         `json:"version"`
	Rules    string         `json:"rules"`
	Recorded int            `json:"recordedDecisions"`
	Missing  int            `json:"missingDecisions"`
	Actions  map[string]int `json:"actions"`
}

func auditQueryCandidates(queryID string, candidates []research.Candidate) (queryRecordAudit, bool) {
	a := queryRecordAudit{QueryID: queryID, Groups: []candidateRecordGroup{}}
	seenCandidates, seenRecords := map[string]bool{}, map[string]bool{}
	for _, c := range candidates {
		if c.ID == "" || seenCandidates[c.ID] || len(c.Records) == 0 {
			return a, false
		}
		seenCandidates[c.ID] = true
		if c.RecordIdentity != nil && c.RecordIdentity.Version == research.RecordIdentityVersion {
			if a.Identity == nil {
				a.Identity = &recordIdentitySummary{Version: research.RecordIdentityVersion}
			}
			a.Identity.RecordedCandidates++
			view := research.PreviewRecordIdentity(c.RecordIdentity)
			a.Identity.RoleEntries += view.Preview.RoleCount
			a.Identity.RelationEntries += view.Preview.RelationCount
		}
		g := candidateRecordGroup{RecordIdentity: research.PreviewRecordIdentity(c.RecordIdentity), CandidateID: c.ID, Records: []candidateAuditRecord{}}
		for _, r := range c.Records {
			if r.ID == "" || seenRecords[r.ID] {
				return a, false
			}
			seenRecords[r.ID] = true
			record := candidateAuditRecord{r.ID, r.Work.SourceID, r.Work.SourceRecordID, research.CandidateAliases(r.Work), nil}
			rules, supported := "", false
			if r.Grouping != nil {
				rules, supported = research.RecordGroupingRulesFor(r.Grouping.Version)
			}
			if supported && r.Grouping.CandidateID == c.ID && (a.Grouping == nil || a.Grouping.Version == r.Grouping.Version) {
				record.Grouping = r.Grouping
				if a.Grouping == nil {
					a.Grouping = &recordGroupingSummary{Version: r.Grouping.Version, Rules: rules, Actions: map[string]int{}}
				}
				a.Grouping.Recorded++
				a.Grouping.Actions[r.Grouping.Action]++
			}
			g.Records = append(g.Records, record)
		}
		sort.Slice(g.Records, func(i, j int) bool { return g.Records[i].ID < g.Records[j].ID })
		a.Groups = append(a.Groups, g)
		a.SourceRecords += len(g.Records)
	}
	sort.Slice(a.Groups, func(i, j int) bool { return a.Groups[i].CandidateID < a.Groups[j].CandidateID })
	if a.Identity != nil {
		priority := func(h *research.RecordIdentity) int {
			p := 99
			if h == nil {
				return p
			}
			for _, r := range h.Relations {
				if n := research.IdentityHintPriority(r.Kind); n < p {
					p = n
				}
			}
			for _, r := range h.Roles {
				if n := research.IdentityHintPriority(r.Kind); n < p {
					p = n
				}
			}
			return p
		}
		indices := []int{}
		for i, g := range a.Groups {
			if g.RecordIdentity != nil && (len(g.RecordIdentity.Roles) > 0 || len(g.RecordIdentity.Relations) > 0) {
				indices = append(indices, i)
			} else {
				a.Groups[i].RecordIdentity = nil
			}
		}
		sort.Slice(indices, func(i, j int) bool {
			a, b := a.Groups[indices[i]], a.Groups[indices[j]]
			pa, pb := priority(a.RecordIdentity), priority(b.RecordIdentity)
			if pa != pb {
				return pa < pb
			}
			return a.CandidateID < b.CandidateID
		})
		for _, i := range indices {
			if a.Identity.RetainedCandidateDetails < 16 {
				a.Identity.RetainedCandidateDetails++
			} else {
				a.Groups[i].RecordIdentity = nil
				a.Identity.OmittedCandidateDetails++
			}
		}
	}
	a.Candidates = len(a.Groups)
	a.GroupedRecords = a.SourceRecords - a.Candidates
	if a.Identity != nil {
		a.Identity.MissingCandidates = a.Candidates - a.Identity.RecordedCandidates
	}
	if a.Grouping != nil {
		a.Grouping.Missing = a.SourceRecords - a.Grouping.Recorded
	}
	return a, true
}

// Only bind new attempts. Audit saved query snapshots, never the mutable
// project-wide bibliography. Failure to read audit data is disclosed, not a
// reason to discard already collected scientific evidence.
func (s *RuntimeService) bindResearchRecordAudit(ctx context.Context, d RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	// Upstream receives counts/interpretation only, not the full mapping.
	// These host facts are excluded from batch checkpoint identity so a
	// temporarily unavailable metadata reader cannot invalidate accepted work.
	upstream := node.ID == "evidence_screening" || node.ID == "method_selection"
	if !upstream && !usesReportProvenance(node) && !isIndependentReviewSchema(node.OutputSchema) {
		return input
	}
	fields := decodeObject(input)
	if isIndependentReviewSchema(node.OutputSchema) {
		if report := findStepByNode(d.Steps, "report_drafting"); report != nil && report.Status == StepCompleted {
			var saved map[string]json.RawMessage
			if json.Unmarshal(report.Input, &saved) == nil && len(saved["researchRecordAudit"]) > 0 {
				fields["researchRecordAudit"] = saved["researchRecordAudit"]
				var audit researchRecordAudit
				if json.Unmarshal(saved["researchRecordAudit"], &audit) == nil {
					fields["researchRecordFacts"] = compactResearchRecordFacts(audit)
				}
				return mustJSON(fields)
			}
		}
	}
	ids := appendUnique(nil, literatureState(d).QueryIDs...)
	if discovery, queryIDs, _, _ := literatureDiscovery(d); discovery.Status == StepCompleted {
		ids = appendUnique(ids, queryIDs...)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return input
	}
	a := researchRecordAudit{Version: recordAuditVersion, Status: "available", Queries: []queryRecordAudit{}, Interpretation: "仅复核已保存来源记录到候选的归并，不是独立试验去重、综述重叠核查或穷尽检索。每个query单独计数，跨query不得相加为独立题录。records列出保存的归属及原记录规范化标识；不能凭当前标识倒推未保存的历史匹配分支。缺失信息不从候选总数猜测。identityHints仅为题录线索条目统计（两端可分别记录，不能相加为独立关系或研究数），recordIdentity仍待科学核对；没有提示不证明独立。"}
	if s.literature == nil {
		a.Status = "unavailable"
	} else {
		for _, id := range ids {
			values, err := s.literature.LiteratureCandidates(ctx, d.Run.ProjectID, []string{id})
			if err != nil {
				a.Status = "incomplete"
				continue
			}
			valid := true
			for _, c := range values {
				if c.ProjectID != "" && c.ProjectID != d.Run.ProjectID {
					valid = false
				}
				for _, r := range c.Records {
					if r.ProjectID != "" && r.ProjectID != d.Run.ProjectID {
						valid = false
					}
				}
			}
			q, ok := auditQueryCandidates(id, values)
			if !ok || !valid {
				a.Status = "incomplete"
				continue
			}
			a.Queries = append(a.Queries, q)
		}
	}
	fields["researchRecordFacts"] = compactResearchRecordFacts(a)
	if !upstream {
		fields["researchRecordAudit"] = a
	}
	return mustJSON(fields)
}

func currentEvidenceWork(detail RunDetail) any {
	s := findStepByNode(detail.Steps, "evidence_screening")
	if s == nil || s.Status != StepCompleted {
		return nil
	}
	var input struct {
		Info selectedEvidenceInput `json:"_selectedEvidence"`
	}
	var output struct {
		Analysis struct {
			Documents []json.RawMessage `json:"documentAnalyses"`
		} `json:"analysis"`
	}
	if json.Unmarshal(s.Input, &input) != nil || json.Unmarshal(s.Output, &output) != nil {
		return nil
	}
	analyses := input.Info.Analyses
	if input.Info.Direct {
		analyses = output.Analysis.Documents
	} else {
		analyses = mergedEvidenceAnalyses(analyses, output.Analysis.Documents)
	}
	if len(analyses) == 0 {
		return nil
	}
	return map[string]any{"nodeId": s.NodeID, "attempt": s.Attempt, "inputSha256": s.InputSHA256, "outputSha256": hashJSON(s.Output), "documentAnalyses": analyses, "interpretation": "本轮已接受的逐文档分析原文，不是原始文献、独立验证或完成评分的证明。报告按实际内容形成评价表；审查结合原始证据核对，不把未提供判断当成已完成。此处references是分析时的来源记录，不表示用户已采用；报告引用仍仅使用当前evidenceContext，未采用来源支持的主张须补选或保留为待核验。"}
}

func addEvidenceWork(detail RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	if node.ID != "method_selection" && node.ID != "report_drafting" && !isIndependentReviewSchema(node.OutputSchema) {
		return input
	}
	if work := currentEvidenceWork(detail); work != nil {
		v := decodeObject(input)
		v["evidenceWork"] = work
		if canReadEvidenceWork(node) {
			v["_evidenceWorkPaging"] = evidenceWorkPagingVersion
		}
		return mustJSON(v)
	}
	return input
}

// Factual reconciliation guidance, not a scientific acceptance gate.
const researchCurrentFactsInstruction = `
Current record reconciliation: researchRecordFacts/researchRecordAudit describe saved source records mapped to candidates. When status=available, their per-query counts/mapping supersede an upstream or historical assertion that those particular saved records are unavailable. Only an explicit groupingMethod and per-record grouping decision establish the recorded matching procedure for that query; reuse_source_record records reuse of an existing source identity, NOT a reconstruction of its original matching branch. Missing/unknown decisions remain unknown. If recordIdentity is present, its catalogue hints remain unverified, not automatic merges/exclusions or independent-study counts; no hint is not proof of independence. None establishes unique trials, review overlap or exhaustive search. Do not infer an executed method from a proposed route or counts, or call all grouping unavailable when its actual method is recorded. Do not sum across queries as independent studies. A status=incomplete/unavailable is a read limitation, not proof the records never existed.
The evidenceWork documentAnalyses/workProduct are actual upstream work to verify and present when required, not merely a future plan. Reuse a supported existing appraisal instead of replacing it with a generic limitations paragraph. Missing presentation with a correct upstream product is a report edit, not automatic evidence re-execution. A relationship between trial reports requires identity evidence (registration/primary report and compatible participants/timepoints); similar topics or approximate weight change alone do not prove the same trial. Preserve uncertainty where identity remains unverified.
`

func compactResearchRecordFacts(a researchRecordAudit) researchRecordAudit {
	a.Queries = append([]queryRecordAudit(nil), a.Queries...)
	for i := range a.Queries {
		a.Queries[i].Groups = nil
	}
	return a
}
