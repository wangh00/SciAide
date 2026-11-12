package workflow

import (
	"encoding/json"
	"sort"
	"strings"
)

// Compare accepted quotations and named unresolved gaps, not mutable retry
// metadata/input hashes. This observes adopted evidence only; it does not prove
// all accessible sources were read, nor choose a scientific verdict.
func unchangedAdoptedEvidenceRevision(d RunDetail, nodeID string) bool {
	step := findStepByNode(d.Steps, nodeID)
	if step == nil || step.Status != StepCompleted {
		return false
	}
	current, err := outputPort(step.Output, "analysis")
	if err != nil {
		return false
	}
	signature := func(output json.RawMessage) string {
		var v struct {
			Gaps        []evidenceGap `json:"evidenceGaps"`
			Assessments []struct {
				Reference string `json:"reference"`
				Decision  string `json:"decision"`
				Quote     string `json:"supportingQuote"`
				Level     string `json:"sourceLevel"`
			} `json:"citationAssessments"`
		}
		if json.Unmarshal(output, &v) != nil {
			return ""
		}
		gaps, quotes := []string{}, []string{}
		for _, g := range v.Gaps {
			if g.Required && !evidenceGapResolvedForScope(g) {
				gaps = append(gaps, g.Field+"\x00"+g.Status)
			}
		}
		for _, a := range v.Assessments {
			if strings.TrimSpace(a.Quote) != "" {
				quotes = append(quotes, a.Reference+"\x00"+a.Decision+"\x00"+a.Level+"\x00"+a.Quote)
			}
		}
		if len(gaps) == 0 || len(quotes) == 0 {
			return ""
		}
		sort.Strings(gaps)
		sort.Strings(quotes)
		return hashJSON(mustJSON([][]string{gaps, quotes}))
	}
	key := signature(current)
	if key == "" {
		return false
	}
	for i := len(d.Events) - 1; i >= 0; i-- {
		e := d.Events[i]
		if e.Type == "workflow.user_revision_queued" {
			return false
		}
		if e.Type != "workflow.review_revision_queued" {
			continue
		}
		var r reviewRevision
		if json.Unmarshal(e.Payload, &r) != nil || r.ProducerNodeID != nodeID || r.ProducerStepID != step.ID || r.NextProducerAttempt < 1 || step.Attempt < r.NextProducerAttempt || hashJSON(r.PriorSubject) != r.PriorSubjectSHA256 || hashJSON(r.IndependentReview) != r.ReviewOutputSHA256 {
			return false
		}
		return signature(r.PriorSubject) == key
	}
	return false
}

// Advisory only: detect unchanged scientific input, not unchanged prose or
// keywords. A fresh review may legitimately repeat an issue after real changes.
func reviewRepairProgress(d RunDetail, nodeID string) string {
	node := compilationNodeMap(d.Run.Compilation)[nodeID]
	if nodeID == "evidence_screening" {
		if unchangedAdoptedEvidenceRevision(d, nodeID) {
			return "本轮与已确认返修前采用的引文原文及核心缺口记录相同，未观察到这部分采用结果进展；不等于所有可访问来源均已核读。再次补查应具体说明尚未尝试的来源/范围及不同操作，不能仅重复综合。先区分原承诺必须完成的工作与可如实报告的不确定结论；确需不可取得的材料或范围决定时用原有补充信息流程，不新增核对轮次。"
		}
		canRead := false
		for _, t := range node.AllowedTools {
			canRead = canRead || t.QualifiedName == "builtin.knowledge.read"
		}
		_, rounds := supplementaryEvidenceQueries(d)
		var outputs []AIExecution
		step := findStepByNode(d.Steps, nodeID)
		if step == nil {
			return ""
		}
		for _, e := range d.AIExecutions {
			if e.WorkflowStepID == step.ID && e.Status == "completed" && schemaDeclaresPropertyValue(e.Output, "coverage") {
				outputs = append(outputs, e)
			}
		}
		sort.Slice(outputs, func(i, j int) bool { return outputs[i].Attempt < outputs[j].Attempt })
		if !canRead && rounds >= 2 && len(outputs) > 0 {
			var latest struct {
				Gaps []evidenceGap `json:"evidenceGaps"`
			}
			_ = json.Unmarshal(outputs[len(outputs)-1].Output, &latest)
			for _, g := range latest.Gaps {
				if g.Required && !evidenceGapResolvedForScope(g) {
					return "当前冻结路线没有已选原文页章核读入口，且关键词补查已用尽；重复综合不能补新证据。请在本次审查内判断是否仍需原文或调整范围；只有实际缺少的材料或必要选择才需要用户补充。"
				}
			}
		}
		if len(outputs) >= 2 {
			previous, current := outputs[len(outputs)-2], outputs[len(outputs)-1]
			// Conservatively require identical frozen inputs, not merely the
			// same recommended markers. New source/scope/revision inputs count.
			if current.InputSHA256 == "" || current.InputSHA256 != previous.InputSHA256 {
				return ""
			}
			a, b := decodeObject(previous.Output), decodeObject(current.Output)
			// Query wording and commentary can change without changing sources.
			gapSignature := func(v any) json.RawMessage {
				var gs []evidenceGap
				_ = json.Unmarshal(mustJSON(v), &gs)
				items := []map[string]any{}
				for _, g := range gs {
					if g.Required && !evidenceGapResolvedForScope(g) {
						items = append(items, map[string]any{"field": g.Field, "status": g.Status})
					}
				}
				return mustJSON(items)
			}
			ga, gb := gapSignature(a["evidenceGaps"]), gapSignature(b["evidenceGaps"])
			if string(gb) != "[]" && hashJSON(ga) == hashJSON(gb) && hashJSON(mustJSON(a["recommendedReferences"])) == hashJSON(mustJSON(b["recommendedReferences"])) {
				return "本次与上一轮保留相同核心缺口及引用；未观察到补证进展。请在本次审查内核对相关页章及修正依据，判断负责阶段；这条历史提示本身不代表缺少用户输入。"
			}
		}
	}
	return ""
}

func schemaDeclaresPropertyValue(rawValue json.RawMessage, key string) bool {
	_, ok := decodeObject(rawValue)[key]
	return ok
}
