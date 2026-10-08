package workflow

import (
	"encoding/json"
	"sort"
)

// Advisory only: detect unchanged scientific input, not unchanged prose or
// keywords. A fresh review may legitimately repeat an issue after real changes.
func reviewRepairProgress(d RunDetail, nodeID string) string {
	node := compilationNodeMap(d.Run.Compilation)[nodeID]
	if nodeID == "evidence_screening" {
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
					return "当前冻结路线没有已选原文页章核读入口，且关键词补查已用尽；重复综合不能补新证据。请在聊天中决定补充材料或新建具备核读能力的任务，不会默认再跑同一路径。"
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
				return "本次与上一轮保留相同核心缺口及引用；未观察到补证进展。请先核读相关页章或讨论范围，暂不推荐重复整链返工。"
			}
		}
	}
	return ""
}

func schemaDeclaresPropertyValue(rawValue json.RawMessage, key string) bool {
	_, ok := decodeObject(rawValue)[key]
	return ok
}
