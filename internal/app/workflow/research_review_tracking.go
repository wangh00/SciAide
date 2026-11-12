package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

type reviewBasis struct {
	Pointer string `json:"pointer"`
	Quote   string `json:"quote"`
}

type trackedReviewFinding struct {
	ID              string        `json:"id"`
	Status          string        `json:"status"`
	Category        string        `json:"category"`
	Summary         string        `json:"summary"`
	Location        string        `json:"location"`
	Basis           []reviewBasis `json:"basis"`
	Reason          string        `json:"reason"`
	Origin          string        `json:"origin"`
	ChangeReason    string        `json:"changeReason"`
	CorrectionIndex int           `json:"correctionIndex"`
}

type reviewHistory struct {
	IssuePrefix          string                     `json:"issuePrefix"`
	PreviousAttempt      int                        `json:"previousAttempt"`
	PreviousInputSHA256  string                     `json:"previousInputSha256,omitempty"`
	PreviousOutputSHA256 string                     `json:"previousOutputSha256,omitempty"`
	Findings             []trackedReviewFinding     `json:"findings"`
	VerifiedClaims       []string                   `json:"verifiedClaims"`
	AcceptanceChecks     []ResearchAcceptanceCheck  `json:"acceptanceChecks,omitempty"`
	RequiredCorrections  []string                   `json:"requiredCorrections,omitempty"`
	RevisionPlan         []researchRevisionPlanItem `json:"revisionPlan,omitempty"`
}

// New dynamic routes opt in; frozen schemas and historical outputs stay intact.
func trackedResearchReviewSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(researchAcceptanceReviewSchema(), &schema)
	properties := schema["properties"].(map[string]any)
	properties["reviewFindings"] = decodeObject(raw(`{"type":"array","maxItems":100,"items":{"type":"object","additionalProperties":false,"required":["id","status","category","summary","location","basis","reason","origin","changeReason","correctionIndex"],"properties":{"id":{"type":"string","pattern":"^issue-[1-9][0-9]*-[1-9][0-9]*$","maxLength":80},"status":{"type":"string","enum":["open","resolved","withdrawn"]},"category":{"type":"string","enum":["unsupportedClaims","citationIssues","numericIssues","methodIssues"]},"summary":{"type":"string","minLength":1,"maxLength":2000},"location":{"type":"string","minLength":1,"maxLength":1000},"basis":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","additionalProperties":false,"required":["pointer","quote"],"properties":{"pointer":{"type":"string","minLength":1,"maxLength":500},"quote":{"type":"string","minLength":1,"maxLength":2000}}}},"reason":{"type":"string","minLength":1,"maxLength":2000},"origin":{"type":"string","enum":["new","existing","reopened"]},"changeReason":{"type":"string","maxLength":2000},"correctionIndex":{"type":"integer","minimum":-1,"maximum":99}}}}`))
	properties["suggestions"] = decodeObject(raw(`{"type":"array","maxItems":30,"items":{"type":"string","minLength":1,"maxLength":2000}}`))
	schema["required"] = append(schema["required"].([]any), "reviewFindings", "suggestions", "revisionPlan")
	return mustJSON(schema)
}

func usesTrackedReview(node CompiledNode) bool {
	return isIndependentReviewSchema(node.OutputSchema) && schemaDeclaresProperty(node.OutputSchema, "reviewFindings")
}

func trackedReviewHistory(detail RunDetail, node CompiledNode) (reviewHistory, error) {
	history := reviewHistory{Findings: []trackedReviewFinding{}, VerifiedClaims: []string{}}
	step := findStepByNode(detail.Steps, node.ID)
	if step == nil {
		return history, fmt.Errorf("审查问题跟踪缺少当前阶段")
	}
	history.IssuePrefix = fmt.Sprintf("issue-%d-", step.Attempt+1)
	var latest *AIExecution
	for i := range detail.AIExecutions {
		execution := &detail.AIExecutions[i]
		if execution.WorkflowStepID == step.ID && execution.Status == "completed" && execution.Attempt <= step.Attempt && (latest == nil || execution.Attempt > latest.Attempt) {
			latest = execution
		}
	}
	if latest == nil {
		return history, nil
	}
	if latest.OutputSHA256 == "" || hashJSON(latest.Output) != latest.OutputSHA256 || latest.InputSHA256 == "" {
		return history, fmt.Errorf("上一轮审查快照校验失败，不能静默丢弃返修问题")
	}
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, latest.Output); err != nil {
		return history, fmt.Errorf("上一轮审查不符合当前冻结契约：%w", err)
	}
	var previous struct {
		Findings            []trackedReviewFinding     `json:"reviewFindings"`
		VerifiedClaims      []string                   `json:"verifiedClaims"`
		InputHash           string                     `json:"reviewedInputSha256"`
		AcceptanceChecks    []ResearchAcceptanceCheck  `json:"acceptanceChecks"`
		RequiredCorrections []string                   `json:"requiredCorrections"`
		RevisionPlan        []researchRevisionPlanItem `json:"revisionPlan"`
	}
	if json.Unmarshal(latest.Output, &previous) != nil || previous.InputHash != latest.InputSHA256 {
		return history, fmt.Errorf("上一轮审查与所审输入不一致")
	}
	history.PreviousAttempt, history.PreviousInputSHA256, history.PreviousOutputSHA256 = latest.Attempt, latest.InputSHA256, latest.OutputSHA256
	history.Findings, history.VerifiedClaims = previous.Findings, previous.VerifiedClaims
	history.AcceptanceChecks, history.RequiredCorrections, history.RevisionPlan = previous.AcceptanceChecks, previous.RequiredCorrections, previous.RevisionPlan
	return history, nil
}

// Resolve RFC 6901 pointers only in current scientific inputs, never in prior
// opinions. An exact quote proves location, not the scientific inference.
// Requirements explain what must be checked, never what the research proves.
// Keep the frozen output schema unchanged; the host derives the basis role
// from an allowlisted path, not from a model-supplied classification.
func reviewBasisAt(input map[string]any, pointer string) (any, bool, error) {
	parts := strings.Split(pointer, "/")
	if len(parts) < 2 || parts[0] != "" {
		return nil, false, fmt.Errorf("依据路径格式无效：%s；请使用当前输入的 RFC 6901 路径", pointer)
	}
	allowed := map[string]bool{"context": true, "evidenceContext": true, "evidenceScreening": true, "evidenceSelectionAudit": true, "researchContract": true, "researchSourceContext": true, "methodContext": true, "implementationContext": true, "computedResults": true, "dataPreflight": true, "sourceArtifacts": true, "designContext": true, "acceptanceCriteria": true, "originalRequest": true, "routeContext": true}
	// New host records and analysis content may be located, but their identity
	// and explanatory metadata cannot alone establish scientific facts.
	allowed["researchRecordAudit"] = true
	allowed["evidenceWork"] = true
	// Separated record text remains reviewable, not its provenance metadata.
	if parts[1] == "reportRecordAppendices" && len(parts) == 3 {
		var composition reportComposition
		_ = json.Unmarshal(mustJSON(input["reportComposition"]), &composition)
		allowed[parts[1]] = composition.ViewVersion == reportReviewPartsVersion
	}
	// Only the original decision text is evidence of a saved screening action.
	// Navigation, host observations and repair ownership never prove science.
	if parts[1] == "researchHandoff" && len(parts) == 5 && parts[2] == "excludedRecords" && (parts[4] == "reason" || parts[4] == "candidateId") {
		allowed["researchHandoff"] = true
	}
	for i := 1; i < len(parts); i++ {
		for j := 0; j < len(parts[i]); j++ {
			if parts[i][j] == '~' {
				if j+1 >= len(parts[i]) || (parts[i][j+1] != '0' && parts[i][j+1] != '1') {
					return nil, false, fmt.Errorf("依据路径转义无效：%s；仅支持 ~0 和 ~1", pointer)
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(parts[i], "~1", "/"), "~0", "~")
	}
	// Authorization may be quoted as workflow state, never scientific support.
	allowed["implementationDecision"] = true
	allowed["dataBinding"] = true
	requirement := strings.HasPrefix(pointer, "/researchSourceContext/indexedReadRanges/") || parts[1] == "implementationDecision" || parts[1] == "dataBinding" || parts[1] == "researchContract" || parts[1] == "acceptanceCriteria" || parts[1] == "originalRequest" || parts[1] == "routeContext"
	if parts[1] == "evidenceWork" {
		requirement = len(parts) < 3 || parts[2] != "documentAnalyses"
	}
	if parts[1] == "researchRecordAudit" {
		requirement = len(parts) < 3 || parts[2] != "queries"
	}
	if parts[1] == "_userRevision" {
		// Deliberately exclude reason, priorOutput and arbitrary nested content.
		if len(parts) != 4 || parts[2] != "changes" {
			return nil, false, fmt.Errorf("依据路径不允许：%s；返修要求仅可引用 /_userRevision/changes/数字下标，不可引用历史结果或返修理由", pointer)
		}
		index, err := strconv.Atoi(parts[3])
		if err != nil || index < 0 || strconv.Itoa(index) != parts[3] {
			return nil, false, fmt.Errorf("返修要求下标无效：%s；请使用从 0 开始的整数下标", pointer)
		}
		requirement = true
	} else if !allowed[parts[1]] {
		if parts[1] == "researchHandoff" {
			return nil, false, fmt.Errorf("依据路径不允许：%s；researchHandoff 仅可引用 /researchHandoff/excludedRecords/数字下标/reason 或 candidateId 的原筛选记录。attributionObservations、navigation 和 repairResultRoots 只提供定位，不能充当事实证据；请引用 /context 当前交付稿或 /researchSourceContext 的原始书目、来源摘录", pointer)
		}
		return nil, false, fmt.Errorf("依据路径不允许：%s；历史审查意见不能充当事实证据，请改引 /context 的当前交付稿或当前证据字段；返修要求只允许 /_userRevision/changes/数字下标", pointer)
	}
	var current any = input
	for _, part := range parts[1:] {
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[part]
			if !ok {
				return nil, false, fmt.Errorf("依据路径不存在：%s；请核对当前输入字段名及数组下标", pointer)
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) || strconv.Itoa(index) != part {
				return nil, false, fmt.Errorf("依据路径不存在：%s；请核对当前输入字段名及数组下标", pointer)
			}
			current = value[index]
		default:
			return nil, false, fmt.Errorf("依据路径不存在：%s；不能继续遍历标量值", pointer)
		}
	}
	return current, requirement, nil
}

func validateTrackedReview(output, input json.RawMessage) error {
	var stage struct {
		History *reviewHistory `json:"reviewHistory"`
	}
	var value struct {
		Approved    bool                   `json:"approved"`
		Findings    []trackedReviewFinding `json:"reviewFindings"`
		Corrections []string               `json:"requiredCorrections"`
	}
	if json.Unmarshal(input, &stage) != nil || stage.History == nil || stage.History.IssuePrefix == "" || json.Unmarshal(output, &value) != nil {
		return fmt.Errorf("审查问题跟踪缺少冻结输入或输出")
	}
	checks := []error{reviewBasisDiagnostics(value.Findings, input), reviewFindingStateDiagnostics(value.Findings, stage.History)}
	problems := []string{}
	addProblem := func(format string, values ...any) {
		problems = append(problems, fmt.Sprintf(format, values...))
	}
	previous := map[string]trackedReviewFinding{}
	for _, finding := range stage.History.Findings {
		previous[finding.ID] = finding
	}
	seen, corrections := map[string]bool{}, map[int]bool{}
	classified := map[string][]string{}
	fields := decodeObject(output)
	for _, category := range []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"} {
		var items []string
		_ = json.Unmarshal(mustJSON(fields[category]), &items)
		classified[category] = items
	}
	root := decodeObject(input)
	open := 0
	for _, finding := range value.Findings {
		if seen[finding.ID] || strings.TrimSpace(finding.Summary) == "" || strings.TrimSpace(finding.Reason) == "" || strings.TrimSpace(finding.Location) == "" {
			addProblem("审查问题 %s 编号重复或缺少定位、理由", finding.ID)
		}
		seen[finding.ID] = true
		if len(finding.Basis) == 0 {
			addProblem("问题 %s 缺少当前输入依据", finding.ID)
		}
		hasRequirement, hasEvidence, hasDeliverable := false, false, false
		basisValid := len(finding.Basis) > 0
		for _, basis := range finding.Basis {
			located, requirement, err := reviewBasisAt(root, basis.Pointer)
			if err != nil || !reviewBasisQuoteMatches(located, basis.Quote) {
				// Already diagnosed with the exact source/path. Still check
				// independent IDs, correspondence and verdict in this turn.
				basisValid = false
				continue
			}
			hasRequirement = hasRequirement || requirement
			hasEvidence = hasEvidence || !requirement
			hasDeliverable = hasDeliverable || strings.HasPrefix(basis.Pointer, "/context/") || strings.HasPrefix(basis.Pointer, "/reportRecordAppendices/")
		}
		if basisValid && hasRequirement && !hasEvidence {
			addProblem("问题 %s 仅引用了验收/返修要求；要求不是科学事实，也不能证明已落实。请补充当前交付稿 /context、分离记录 /reportRecordAppendices/数字下标或当前证据字段的原文依据", finding.ID)
		}
		if basisValid && finding.Status == "resolved" && !hasDeliverable {
			addProblem("问题 %s 判定已解决时，必须同时引用 /context 正文或 /reportRecordAppendices/数字下标当前记录的原文；不能仅凭要求或来源材料宣称已改正", finding.ID)
		}
		if finding.Status == "open" {
			open++
			i := finding.CorrectionIndex
			if i < 0 || i >= len(value.Corrections) || corrections[i] {
				addProblem("问题 %s 必须唯一对应一项 requiredCorrections", finding.ID)
			} else {
				corrections[i] = true
			}
			items := classified[finding.Category]
			matched := false
			for index, item := range items {
				if item == finding.Summary {
					classified[finding.Category] = append(items[:index], items[index+1:]...)
					matched = true
					break
				}
			}
			if !matched {
				addProblem("问题 %s 的 summary 必须与对应分类问题原文一致", finding.ID)
			}
		} else if (finding.Status != "resolved" && finding.Status != "withdrawn") || finding.CorrectionIndex != -1 {
			addProblem("已解决或撤回的问题 %s 不得要求返修", finding.ID)
		}
	}
	for id := range previous {
		if !seen[id] {
			checks = append(checks, &ReviewMissingFindingError{ID: id})
		}
	}
	for _, items := range classified {
		if len(items) > 0 {
			addProblem("每条阻断意见必须有可追踪问题及当前依据")
			break
		}
	}
	if len(corrections) != len(value.Corrections) || value.Approved != (open == 0) {
		addProblem("审查结论、未解决问题和必改清单不一致")
	}
	if len(problems) > 0 {
		checks = append(checks, fmt.Errorf("审查问题对应关系需要一并修正：\n%s", strings.Join(problems, "\n")))
	}
	return errors.Join(checks...)
}

// Carries identity only. The reviewer must supply a current decision and
// evidence; the host must never copy a historical judgment to fill this gap.
type ReviewMissingFindingError struct{ ID string }

func (e *ReviewMissingFindingError) Error() string {
	return fmt.Sprintf("必须逐项核对上一轮问题 %s，不能遗漏已解决或未解决项；只需补交该问题的当前判断，保留其它问题", e.ID)
}

// State explanation and evidence attribution are independent defects. Report
// all of them in the same correction rather than spending one turn on each.
// Never synthesize changeReason: it is the reviewer's accountable assessment.
type ReviewFindingStateErrors struct {
	Problems    []string
	RepairPaths []string
}

func (e *ReviewFindingStateErrors) Error() string {
	return "审查问题状态需要一并修正：\n" + strings.Join(e.Problems, "\n")
}

func reviewFindingStateDiagnostics(findings []trackedReviewFinding, history *reviewHistory) error {
	previous := map[string]trackedReviewFinding{}
	for _, finding := range history.Findings {
		previous[finding.ID] = finding
	}
	result := &ReviewFindingStateErrors{}
	for i, finding := range findings {
		prior, exists := previous[finding.ID]
		if !exists {
			if !strings.HasPrefix(finding.ID, history.IssuePrefix) || finding.Origin != "new" || finding.Status != "open" {
				result.Problems = append(result.Problems, fmt.Sprintf("新增问题 %s 必须使用本轮编号并标为 open/new", finding.ID))
			}
		} else {
			expected := "existing"
			if prior.Status != "open" && finding.Status == "open" {
				expected = "reopened"
			}
			if finding.Origin != expected {
				result.Problems = append(result.Problems, fmt.Sprintf("问题 %s 的 origin 必须为 %s，按当前依据说明状态变化", finding.ID, expected))
			}
		}
		if (!exists || finding.Status != prior.Status) && strings.TrimSpace(finding.ChangeReason) == "" {
			result.Problems = append(result.Problems, fmt.Sprintf("问题 %s 的 changeReason 为空；请说明本轮新增依据或状态变化的当前依据，保留已正确的 basis", finding.ID))
			result.RepairPaths = append(result.RepairPaths, fmt.Sprintf("/reviewFindings/%d/changeReason", i))
		}
	}
	if len(result.Problems) != 0 {
		return result
	}
	return nil
}

const trackedReviewInstruction = `
Current /dataBinding scalar fields may be quoted only as host file-adoption state, not scientific evidence. Pair them with current deliverable/method/result evidence; binding is not proof of calculation or scientific validity. A /dataBinding pointer is available only when that field actually occurs in the frozen input.
Basis path policy: /_userRevision/changes/<zero-based index> may be quoted ONLY as a user-confirmed requirement, never as scientific evidence. /researchContract, /acceptanceCriteria, /originalRequest and /routeContext are also requirements. Whenever citing a requirement, include a separate exact quote from the current deliverable /context or current scientific evidence. To mark ANY finding resolved, an exact quote from the current deliverable /context or a separated /reportRecordAppendices/N record is mandatory; a request to change something is not proof it was changed. Do not cite other _userRevision fields (including reason or priorOutput). Allowed current evidence roots: context, evidenceContext, evidenceScreening, evidenceSelectionAudit, researchSourceContext, methodContext, implementationContext, computedResults, dataPreflight, sourceArtifacts, designContext; evidenceWork/documentAnalyses is analysis to verify, not original evidence; researchRecordAudit/queries only proves saved record grouping, not trial independence. Their other metadata is host state and must be paired with actual evidence. reportRecordAppendices/N only in the host-bound separated report view. Pointer existence, allowed source, scalar type and exact quotation are checked separately. Exact textual matching proves only provenance, not the scientific validity of a conclusion.
Track reviewFindings against stage_input.reviewHistory. Return every prior finding exactly once with its stable id and current status open/resolved/withdrawn. History is prior opinion, not scientific truth. Recheck the current deliverable independently, including previously passed claims. Each finding needs category, summary, location, reason, and 1-4 basis entries: RFC 6901 pointer to a current input string (or scalar number/bool), plus an exact quote from that value. In the separated report view, /context contains the authored body and /reportRecordAppendices/N contains current host-managed record text; both are valid current-deliverable bases, not proof of scientific validity. Do not use reviewHistory, revisionTargets or prior reviewer opinions as evidence. For missing content, quote the applicable requirement/current method and explain the absence; never invent a missing quote.
New findings use reviewHistory.issuePrefix plus a positive integer, origin=new, status=open and changeReason explaining first detection, prior omission or a revision-introduced defect. Existing findings use origin=existing; reopening a closed finding uses origin=reopened. Explain every status change and every reversal of a previously passed judgment with concrete current evidence. A shared research team does not establish overlapping samples; distinct registrations alone do not establish independence either.
Only unresolved defects affecting correctness, provenance, reproducibility or frozen acceptance criteria block delivery. Each open finding must correspond to exactly one requiredCorrections entry via correctionIndex, the host derives category issue arrays from these findings. Closed findings use correctionIndex=-1 and must not appear in those arrays. Put optional wording/style improvements in suggestions, not blockers; preserve scientific caveats in limitations. Do not lower acceptance criteria or excuse missing core evidence. Do not rename a repeated problem to make it new. Do not demand a fresh search for a missing description when actual retrieval logs already supply it.
Resolved means the current result fixes the defect. Withdrawn means the prior objection was unsound, not that it was inconvenient; explain and support withdrawal. Finding reasons/statuses stay in the review audit, never in the research report. Do not claim certainty beyond the supplied evidence.`
