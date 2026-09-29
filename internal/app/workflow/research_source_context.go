package workflow

import (
	"encoding/json"
)

// Persist discovery and bibliographic provenance through report and review.
func researchSourceContext(detail RunDetail) map[string]any {
	result := map[string]any{}
	queries, rounds := supplementaryEvidenceQueries(detail)
	result["localEvidenceFollowup"] = map[string]any{"roundsUsed": rounds, "roundsRemaining": max(0, 2-rounds), "queries": queries, "absenceProven": false}
	result["interpretation"] = "检索日志是已执行操作的来源记录，不证明系统综述穷尽；书目信息用于题录追溯，不是研究结果证据。selectionAudit的distinctDocumentCount仅为文档数，independentStudyCountVerified=false时不能把兼容字段independentStudyCount当成独立试验数。PDF可用不等于全文已读。indexedMaterials 是本轮实际已导入且就绪的索引，覆盖前期尚未导入的假设；未检索到不等于原文未报告，不得要求重复上传已存在的全文。"
	for _, s := range detail.Steps {
		if s.Status != StepCompleted {
			continue
		}
		o := decodeObject(s.Output)
		if s.NodeID == "literature_discovery" {
			v := decodeObject(mustJSON(o["structured"]))
			result["retrieval"] = map[string]any{"queries": v["queries"], "sources": v["sources"], "candidateCount": v["candidateCount"], "completedAt": s.CompletedAt, "firstPageOnly": true}
		}
		if s.NodeID == "candidate_review" {
			result["selection"] = o
		}
		if s.NodeID == "evidence_sync" {
			result["indexedMaterials"] = decodeObject(mustJSON(o["structured"]))
		}
		if s.NodeID == "evidence_search" {
			result["retrievedEvidence"] = decodeObject(mustJSON(o["structured"]))
		}
		if s.NodeID == "candidate_screening" {
			result["bibliography"] = o["candidates"]
		}
	}
	return result
}
func addResearchSourceContext(detail RunDetail, node CompiledNode, input json.RawMessage) json.RawMessage {
	if node.ID != "method_selection" && node.ID != "report_drafting" && node.ID != "independent_review" {
		return input
	}
	args := decodeObject(input)
	args["researchSourceContext"] = researchSourceContext(detail)
	return mustJSON(args)
}
