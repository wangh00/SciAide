package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wangh00/SciAide/internal/app/tool"
)

var reviewCategoryFields = []string{"unsupportedClaims", "citationIssues", "numericIssues", "methodIssues"}

const reviewRepairVersion = "review-repair-v1"

// The provider writes findings once. The persisted schema remains unchanged for
// historical gates, UI and hashes. This is a wire projection, not a migration.
func ReviewSubmissionSchema(schema json.RawMessage) json.RawMessage {
	return unboundReviewSubmissionSchema(issueReviewSubmissionSchema(schema), schema)
}

// ReviewCanonicalSubmissionPreflightSchema recognizes historical complete
// reviews before the host binds its input identity. It is a routing preflight,
// NOT a frozen output schema or permission to accept an unbound review.
func ReviewCanonicalSubmissionPreflightSchema(schema json.RawMessage) json.RawMessage {
	return unboundReviewSubmissionSchema(schema, schema)
}

func unboundReviewSubmissionSchema(projected, original json.RawMessage) json.RawMessage {
	if !isIndependentReviewSchema(original) {
		return projected
	}
	root := decodeObject(projected)
	// Optional for existing explicit submissions, not a model obligation.
	// A supplied mismatch is rejected, never silently substituted.
	withoutRequired(root, "reviewedInputSha256")
	properties := root["properties"].(map[string]any)
	properties["reviewedInputSha256"].(map[string]any)["description"] = "Host-owned identity: omit this field. The host binds the current frozen input; if supplied it must match exactly."
	return mustJSON(root)
}

func legacyReviewSubmissionSchema(schema json.RawMessage) json.RawMessage {
	node := CompiledNode{OutputSchema: schema}
	if !usesTrackedReview(node) {
		return schema
	}
	root := decodeObject(schema)
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		return schema
	}
	for _, key := range reviewCategoryFields {
		delete(properties, key)
	}
	required := []any{}
	items, _ := root["required"].([]any)
	for _, item := range items {
		key, _ := item.(string)
		if !isReviewCategory(key) {
			required = append(required, item)
		}
	}
	root["required"] = required
	return mustJSON(root)
}

func isReviewCategory(key string) bool {
	for _, category := range reviewCategoryFields {
		if category == key {
			return true
		}
	}
	return false
}

func projectReviewSubmission(text string, node CompiledNode) (json.RawMessage, []AIStageNormalization, error) {
	// Accept canonical historical responses too; contradictory duplicate prose is
	// never silently discarded. Ask for the single-source form instead.
	candidate, err := extractStageJSON(text, raw(`{"type":"object","additionalProperties":true}`))
	if err != nil {
		return nil, nil, err
	}
	fields := decodeObject(candidate)
	if isIssueReviewSubmission(fields, node.OutputSchema) {
		converted, err := expandIssueReviewSubmission(candidate, node.OutputSchema)
		if err != nil {
			return nil, nil, err
		}
		value, changes, err := projectReviewSubmission(string(converted), node)
		if err != nil {
			return nil, nil, err
		}
		return value, append([]AIStageNormalization{{Path: "$", Rule: "review_plan_from_stable_findings", BeforeSHA256: hashJSON(candidate), AfterSHA256: hashJSON(converted)}}, changes...), nil
	}
	var findings []trackedReviewFinding
	if err := json.Unmarshal(mustJSON(fields["reviewFindings"]), &findings); err != nil || fields["reviewFindings"] == nil {
		return nil, nil, fmt.Errorf("reviewFindings 必须是完整的问题明细数组")
	}
	derived := map[string][]string{}
	for _, key := range reviewCategoryFields {
		derived[key] = []string{}
	}
	for _, finding := range findings {
		if finding.Status == "open" && isReviewCategory(finding.Category) {
			derived[finding.Category] = append(derived[finding.Category], finding.Summary)
		}
	}
	for _, key := range reviewCategoryFields {
		if supplied, exists := fields[key]; exists {
			var values []string
			if json.Unmarshal(mustJSON(supplied), &values) != nil {
				return nil, nil, fmt.Errorf("%s 不是问题数组；请省略分类字段，由宿主从 reviewFindings 生成", key)
			}
			remaining := append([]string(nil), derived[key]...)
			for _, value := range values {
				found := false
				for i, want := range remaining {
					if value == want {
						remaining = append(remaining[:i], remaining[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					return nil, nil, fmt.Errorf("分类清单 %s 与问题明细重复且不一致。请将所有真实问题完整保留在 reviewFindings，省略 unsupportedClaims、citationIssues、numericIssues、methodIssues 四个字段；宿主自动生成分类，不要反复改写两份清单", key)
				}
			}
		}
		delete(fields, key)
	}
	compact := mustJSON(fields)
	if err := (tool.JSONSchemaValidator{}).Validate(legacyReviewSubmissionSchema(node.OutputSchema), compact); err != nil {
		return nil, nil, fmt.Errorf("审查提交不符合 Schema: %w", err)
	}
	for _, key := range reviewCategoryFields {
		fields[key] = derived[key]
	}
	normalized := mustJSON(fields)
	if err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, normalized); err != nil {
		return nil, nil, err
	}
	if rawJSONEqual(candidate, normalized) {
		return normalized, nil, nil
	}
	return normalized, []AIStageNormalization{{Path: "$", Rule: "review_categories_from_findings", BeforeSHA256: hashJSON(candidate), AfterSHA256: hashJSON(normalized)}}, nil
}

const singleSourceReviewInstruction = `
Submission transport: follow the CURRENT tool schema. Write each finding once with its stable id. For an open finding place its correction text and revision object (nodeId, reason, whyNotLater, needsUserInput, requiredInput) IN that finding. Resolved/withdrawn findings omit correction and revision. OMIT correctionIndex and the parallel top-level requiredCorrections/revisionPlan and four classification arrays: the host generates positions and arrays from these explicit per-issue decisions, never scientific advice or approval. Keep approved, acceptanceChecks and exact evidence quotes. This supersedes earlier instructions about numeric correctionIndex or repeating category lists. Do not delete a genuine defect to fit the format. Include EVERY historical finding with an explicit current assessment, even resolved/withdrawn. A broad acceptance criterion may cite existing findings instead of duplicating them.
Every new finding, including the FIRST review, needs a non-empty changeReason explaining its current detection. A basis pointer must end at the exact scalar field: for an evidenceGaps item use /evidenceScreening/evidenceGaps/0/field when quoting that field, not /evidenceScreening/evidenceGaps/0. Inspect the actual item; other fields and arrays need their own real scalar paths. Never use this example as evidence or copy an index without checking.
Reading provenance is available at /researchSourceContext/indexedReadRanges when present. It proves returned ranges, not scientific claims or full-document reading. Never cite /indexedReadRanges unless that root actually exists in stage_input. Quote scientific assertions from the current deliverable or actual source excerpts, not document IDs or read ranges.
Limited evidence is not an automatic failure: identify which requested deliverable or actual claim lacks necessary support. A draft that discloses unvalidated measures is not thereby missing; actual promised materials still must be present. Do not demand repeated screening merely because coverage was already disclosed as limited.`

// Repair known transport aliases and uniquely identified whole requirements.
// Never relocate scientific evidence by searching for a matching quote.
// Preserve the scientific content, original input and per-pointer audit.
func canonicalReviewReadPaths(value, input json.RawMessage) (json.RawMessage, []AIStageNormalization) {
	root := decodeObject(input)
	fields := decodeObject(value)
	findings, _ := fields["reviewFindings"].([]any)
	changes := []AIStageNormalization{}
	for i, item := range findings {
		finding, _ := item.(map[string]any)
		bases, _ := finding["basis"].([]any)
		for j, item := range bases {
			basis, _ := item.(map[string]any)
			pointer, _ := basis["pointer"].(string)
			quote, _ := basis["quote"].(string)
			target, rule := canonicalReviewBasisTarget(root, pointer)
			if target == "" {
				if target = canonicalReviewCriterionTarget(root, pointer, quote); target != "" {
					rule = "frozen_criterion_index"
				}
			}
			if target == "" {
				continue
			}
			located, _, err := reviewBasisAt(root, target)
			if rule == "frozen_read_range_path" {
				if _, scalar := located.(string); !scalar {
					continue
				}
			}
			if err != nil || !reviewBasisQuoteMatches(located, quote) {
				continue
			}
			basis["pointer"] = target
			changes = append(changes, AIStageNormalization{Path: fmt.Sprintf("$.reviewFindings[%d].basis[%d].pointer", i, j), Rule: rule, BeforeSHA256: hashJSON(mustJSON(pointer)), AfterSHA256: hashJSON(mustJSON(target))})
		}
	}
	if len(changes) == 0 {
		return value, nil
	}
	return mustJSON(fields), changes
}

// A whole frozen requirement uniquely identifies itself; its array index is
// transport, not source attribution. Limit recovery to /acceptanceCriteria/N/text
// and exact full-string equality. Do not use snippets, history, other fields or
// cross-root matches. The result remains a REQUIREMENT, never scientific proof.
func canonicalReviewCriterionTarget(input map[string]any, pointer, quote string) string {
	parts := strings.Split(pointer, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "acceptanceCriteria" || parts[3] != "text" || strings.TrimSpace(quote) == "" {
		return ""
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 || strconv.Itoa(index) != parts[2] {
		return ""
	}
	if value, _, err := reviewBasisAt(input, pointer); err == nil {
		if _, text := value.(string); !text || reviewBasisQuoteMatches(value, quote) {
			return ""
		}
	}
	criteria, _ := input["acceptanceCriteria"].([]any)
	target := ""
	for i, item := range criteria {
		criterion, _ := item.(map[string]any)
		text, ok := criterion["text"].(string)
		if ok && text == quote {
			if target != "" {
				return "" // Ambiguous, even if the duplicate requirements share IDs.
			}
			target = fmt.Sprintf("/acceptanceCriteria/%d/text", i)
		}
	}
	return target
}

// ReviewBasisErrors distinguishes submission attribution problems from a
// scientific rejection. Full diagnostics remain private to the repair/audit.
type ReviewBasisErrors struct {
	Problems  []string
	Truncated bool
	// Set only by the live diagnostic when every defect is a mismatched
	// quote of an existing string scalar, with no pointer/type failures.
	// RepairPaths remains the legacy pair of pointer and quote coordinates.
	QuoteOnly bool
	// Output cells the model may repair locally. These are not replacement
	// evidence: the merged review still has to pass every frozen check.
	RepairPaths []string
	// Host-issued, current-input-only navigation. Never a replacement judgment.
	Sources []ReviewBasisSource
}

func (e *ReviewBasisErrors) Error() string {
	heading := "审查依据需要一并修正："
	if e.Truncated {
		heading = "审查依据需要修正（本次列出前 8 处）："
	}
	return heading + "\n" + strings.Join(e.Problems, "\n") + "\n请按上述输出单元逐项核对当前字段，摘录连续原文，不拼接、不改写，不用历史审查意见替代当前证据。原文菜单仅作定位；未提供片段不代表原文不存在。"
}

// Even a schema-invalid review can carry independently invalid basis paths.
// Diagnose them in the SAME correction turn, without accepting or filling any
// missing field. Otherwise schema errors consume one of the two corrections
// before the reviewer gets feedback about evidence attribution at all.
func rejectedReviewBasisDiagnostics(text string, input json.RawMessage) error {
	value, err := extractStageJSON(text, raw(`{"type":"object","additionalProperties":true}`))
	if err != nil {
		return nil
	}
	value, _ = canonicalReviewReadPaths(value, input)
	var review struct {
		Findings []trackedReviewFinding `json:"reviewFindings"`
	}
	if json.Unmarshal(value, &review) != nil {
		return nil
	}
	// Citation markers are restored after schema projection at the live boundary.
	// Only diagnose independent path/source/type defects here; matching a quote
	// in the temporary Chat citation domain would give false correction advice.
	var stage struct {
		History *reviewHistory `json:"reviewHistory"`
	}
	var stateErr error
	if json.Unmarshal(input, &stage) == nil && stage.History != nil && stage.History.IssuePrefix != "" {
		stateErr = reviewFindingStateDiagnostics(review.Findings, stage.History)
		seen := map[string]bool{}
		for _, f := range review.Findings {
			seen[f.ID] = true
		}
		for _, f := range stage.History.Findings {
			if !seen[f.ID] {
				stateErr = errors.Join(stateErr, &ReviewMissingFindingError{ID: f.ID})
			}
		}
	}
	return errors.Join(reviewBasisDiagnosticProblems(review.Findings, input, false), stateErr)
}

// Report all independent basis defects in one correction turn. Never rewrite
// quotes by fuzzy matching: a paraphrase may change the scientific meaning.
func reviewBasisDiagnostics(findings []trackedReviewFinding, input json.RawMessage) error {
	return reviewBasisDiagnosticProblems(findings, input, true)
}

func reviewBasisDiagnosticProblems(findings []trackedReviewFinding, input json.RawMessage, checkQuotes bool) error {
	root := decodeObject(input)
	problems := []string{}
	repairPaths := []string{}
	sources := []ReviewBasisSource{}
	sourceBudget := 12000 // Rune budget across all diagnosed cells, not per field.
	quoteOnly := true
	for i, finding := range findings {
		for j, basis := range finding.Basis {
			cell := fmt.Sprintf("/reviewFindings/%d/basis/%d/", i, j)
			before := len(problems)
			value, _, err := reviewBasisAt(root, basis.Pointer)
			if err != nil {
				quoteOnly = false
				hint := ""
				if target, rule := canonicalReviewBasisTarget(root, basis.Pointer); rule == "frozen_kernel_value_path" {
					if _, _, locateErr := reviewBasisAt(root, target); locateErr == nil {
						hint = "；当前 Kernel 正文路径为 " + target + "，请核对该字段并摘录精确标量，不能沿用不匹配的 quote"
					}
				}
				problems = append(problems, fmt.Sprintf("%s %spointer：%v%s", finding.ID, cell, err, hint))
			} else {
				text, scalar := value.(string)
				matched := false
				if scalar {
					matched = strings.TrimSpace(basis.Quote) != "" && strings.Contains(text, basis.Quote)
				} else {
					switch value.(type) {
					case json.Number, float64, bool:
						matched = string(mustJSON(value)) == basis.Quote
						if !matched && checkQuotes {
							quoteOnly = false // Non-text replacement requires the original repair contract.
						}
					default:
						quoteOnly = false
						problems = append(problems, fmt.Sprintf("%s %spointer：依据路径 %s 指向对象或数组；请定位具体文本、数字或布尔值%s", finding.ID, cell, basis.Pointer, reviewScalarChildHint(value, basis.Pointer)))
						matched = true // Already diagnosed; do not also emit a quote mismatch.
					}
				}
				if !matched && checkQuotes {
					// The rejected candidate already contains the full quote. Send
					// every cell, not eight copies of long quotes/instructions.
					problems = append(problems, fmt.Sprintf("%s %squote：引文不匹配，当前来源路径 %s", finding.ID, cell, basis.Pointer))
					if pointer := separatedRecordQuoteHint(root, basis.Pointer, basis.Quote); pointer != "" {
						problems = append(problems, "该引文在分离的程序记录 "+pointer+" 中唯一精确出现；请核对来源角色后使用实际路径，不要据此认定正文重复或自动维持历史意见。")
					}
					if scalar {
						if source := reviewBasisSource(input, cell+"quote", basis.Pointer, text, basis.Quote, &sourceBudget); source != nil {
							sources = append(sources, *source)
						}
					}
				}
			}
			if len(problems) > before {
				repairPaths = append(repairPaths, cell+"pointer", cell+"quote")
			}
		}
	}
	if len(problems) > 0 {
		return &ReviewBasisErrors{Problems: problems, RepairPaths: repairPaths, Sources: sources, QuoteOnly: quoteOnly}
	}
	return nil
}

// Navigation only, never automatic scientific attribution or a verdict. Only
// the known body-to-record split is eligible; no cross-source/history search.
func separatedRecordQuoteHint(root map[string]any, pointer, quote string) string {
	if pointer != "/context/markdown" || strings.TrimSpace(quote) == "" {
		return ""
	}
	var c reportComposition
	_ = json.Unmarshal(mustJSON(root["reportComposition"]), &c)
	if c.ViewVersion != reportReviewPartsVersion {
		return ""
	}
	var records []string
	if json.Unmarshal(mustJSON(root["reportRecordAppendices"]), &records) != nil {
		return ""
	}
	target := ""
	for i, text := range records {
		if strings.Contains(text, quote) {
			if target != "" || strings.Count(text, quote) != 1 {
				return ""
			}
			target = fmt.Sprintf("/reportRecordAppendices/%d", i)
		}
	}
	return target
}

// Show only actual, immediate scalar children of the rejected object. Do not
// search other objects/roots for matching text or choose evidence for the AI.
func reviewScalarChildHint(value any, pointer string) string {
	paths := []string{}
	add := func(key string, child any) {
		switch child.(type) {
		case string, json.Number, float64, bool:
			escaped := strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			childPath := pointer + "/" + escaped
			if len([]rune(childPath)) <= 500 {
				paths = append(paths, childPath)
			}
		}
	}
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			add(key, value[key])
			if len(paths) == 6 {
				break
			}
		}
	case []any:
		for i, child := range value {
			add(strconv.Itoa(i), child)
			if len(paths) == 6 {
				break
			}
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return "；该对象的标量子路径（仅定位，须核对原文）：" + strings.Join(paths, ", ")
}
