package workflow

import (
	"encoding/json"
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
	IssuePrefix          string                 `json:"issuePrefix"`
	PreviousAttempt      int                    `json:"previousAttempt"`
	PreviousInputSHA256  string                 `json:"previousInputSha256,omitempty"`
	PreviousOutputSHA256 string                 `json:"previousOutputSha256,omitempty"`
	Findings             []trackedReviewFinding `json:"findings"`
	VerifiedClaims       []string               `json:"verifiedClaims"`
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
		Findings       []trackedReviewFinding `json:"reviewFindings"`
		VerifiedClaims []string               `json:"verifiedClaims"`
		InputHash      string                 `json:"reviewedInputSha256"`
	}
	if json.Unmarshal(latest.Output, &previous) != nil || previous.InputHash != latest.InputSHA256 {
		return history, fmt.Errorf("上一轮审查与所审输入不一致")
	}
	history.PreviousAttempt, history.PreviousInputSHA256, history.PreviousOutputSHA256 = latest.Attempt, latest.InputSHA256, latest.OutputSHA256
	history.Findings, history.VerifiedClaims = previous.Findings, previous.VerifiedClaims
	return history, nil
}

// Resolve RFC 6901 pointers only in current scientific inputs, never in prior
// opinions. An exact quote proves location, not the scientific inference.
func reviewEvidenceAt(input map[string]any, pointer string) (any, bool) {
	parts := strings.Split(pointer, "/")
	if len(parts) < 2 || parts[0] != "" {
		return nil, false
	}
	allowed := map[string]bool{"context": true, "evidenceContext": true, "evidenceScreening": true, "evidenceSelectionAudit": true, "researchContract": true, "researchSourceContext": true, "methodContext": true, "implementationContext": true, "computedResults": true, "dataPreflight": true, "sourceArtifacts": true, "designContext": true, "acceptanceCriteria": true}
	var current any = input
	for i, part := range parts[1:] {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if i == 0 && !allowed[part] {
			return nil, false
		}
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[part]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(value) || strconv.Itoa(index) != part {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
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
			return fmt.Errorf("审查问题编号重复或缺少定位、理由")
		}
		seen[finding.ID] = true
		prior, exists := previous[finding.ID]
		if !exists {
			if !strings.HasPrefix(finding.ID, stage.History.IssuePrefix) || finding.Origin != "new" || finding.Status != "open" || strings.TrimSpace(finding.ChangeReason) == "" {
				return fmt.Errorf("新增问题 %s 必须使用本轮编号、说明新增依据并标为 open/new", finding.ID)
			}
		} else {
			expected := "existing"
			if prior.Status != "open" && finding.Status == "open" {
				expected = "reopened"
			}
			if finding.Origin != expected || (finding.Status != prior.Status && strings.TrimSpace(finding.ChangeReason) == "") {
				return fmt.Errorf("问题 %s 的状态变化或重新打开必须说明依据", finding.ID)
			}
		}
		if len(finding.Basis) == 0 {
			return fmt.Errorf("问题 %s 缺少当前输入依据", finding.ID)
		}
		for _, basis := range finding.Basis {
			located, ok := reviewEvidenceAt(root, basis.Pointer)
			text, scalar := located.(string)
			if !scalar {
				switch located.(type) {
				case json.Number, float64, bool:
					text = string(mustJSON(located))
				default:
					ok = false
				}
			}
			matches := strings.Contains(text, basis.Quote)
			if !scalar {
				matches = text == basis.Quote
			}
			if !ok || strings.TrimSpace(basis.Quote) == "" || !matches {
				return fmt.Errorf("问题 %s 的依据必须是当前输入 %s 中的原文；历史意见不能充当事实证据", finding.ID, basis.Pointer)
			}
		}
		if finding.Status == "open" {
			open++
			i := finding.CorrectionIndex
			if i < 0 || i >= len(value.Corrections) || corrections[i] {
				return fmt.Errorf("问题 %s 必须唯一对应一项 requiredCorrections", finding.ID)
			}
			corrections[i] = true
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
				return fmt.Errorf("问题 %s 的 summary 必须与对应分类问题原文一致", finding.ID)
			}
		} else if (finding.Status != "resolved" && finding.Status != "withdrawn") || finding.CorrectionIndex != -1 {
			return fmt.Errorf("已解决或撤回的问题 %s 不得要求返修", finding.ID)
		}
	}
	for id := range previous {
		if !seen[id] {
			return fmt.Errorf("必须逐项核对上一轮问题 %s，不能遗漏已解决或未解决项", id)
		}
	}
	for _, items := range classified {
		if len(items) > 0 {
			return fmt.Errorf("每条阻断意见必须有可追踪问题及当前依据")
		}
	}
	if len(corrections) != len(value.Corrections) || value.Approved != (open == 0) {
		return fmt.Errorf("审查结论、未解决问题和必改清单不一致")
	}
	return nil
}

const trackedReviewInstruction = `
Track reviewFindings against stage_input.reviewHistory. Return every prior finding exactly once with its stable id and current status open/resolved/withdrawn. History is prior opinion, not scientific truth. Recheck the current deliverable independently, including previously passed claims. Each finding needs category, summary, location, reason, and 1-4 basis entries: RFC 6901 pointer to a current input string (or scalar number/bool), plus an exact quote from that value. Do not use reviewHistory, revisionTargets or prior reviewer opinions as evidence. For missing content, quote the applicable requirement/current method and explain the absence; never invent a missing quote.
New findings use reviewHistory.issuePrefix plus a positive integer, origin=new, status=open and changeReason explaining first detection, prior omission or a revision-introduced defect. Existing findings use origin=existing; reopening a closed finding uses origin=reopened. Explain every status change and every reversal of a previously passed judgment with concrete current evidence. A shared research team does not establish overlapping samples; distinct registrations alone do not establish independence either.
Only unresolved defects affecting correctness, provenance, reproducibility or frozen acceptance criteria block delivery. Each open finding must correspond to exactly one requiredCorrections entry via correctionIndex, and copy its summary verbatim to exactly one category issue array. Closed findings use correctionIndex=-1 and must not appear in those arrays. Put optional wording/style improvements in suggestions, not blockers; preserve scientific caveats in limitations. Do not lower acceptance criteria or excuse missing core evidence. Do not rename a repeated problem to make it new. Do not demand a fresh search for a missing description when actual retrieval logs already supply it.
Resolved means the current result fixes the defect. Withdrawn means the prior objection was unsound, not that it was inconvenient; explain and support withdrawal. Finding reasons/statuses stay in the review audit, never in the research report. Do not claim certainty beyond the supplied evidence.`
