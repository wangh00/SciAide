package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A user-confirmed delivery amendment is separate from evidence selection.
// It changes acceptance for a new report, never the old question or evidence.
type DeliveryScopeSelection struct {
	Summary         string   `json:"summary"`
	SuccessCriteria []string `json:"successCriteria"`
}
type ResearchDeliveryScope struct {
	Summary          string   `json:"summary"`
	PreviousCriteria []string `json:"previousCriteria"`
	SuccessCriteria  []string `json:"successCriteria"`
}

func currentDeliveryScope(d RunDetail) *ResearchDeliveryScope {
	for i := len(d.Events) - 1; i >= 0; i-- {
		if d.Events[i].Type != "workflow.user_revision_queued" {
			continue
		}
		var event struct {
			Proposal ResearchRevisionProposal `json:"proposal"`
		}
		if json.Unmarshal(d.Events[i].Payload, &event) == nil && event.Proposal.DeliveryScope != nil {
			return event.Proposal.DeliveryScope
		}
	}
	return nil
}
func effectiveDeliveryContract(d RunDetail, contract json.RawMessage) json.RawMessage {
	if scope := currentDeliveryScope(d); scope != nil {
		v := decodeObject(contract)
		v["successCriteria"] = scope.SuccessCriteria
		v["deliveryScope"] = map[string]any{"summary": scope.Summary, "previousCriteria": scope.PreviousCriteria, "interpretation": "用户在返修卡明确确认的本轮交付范围。仅本轮交付验收以successCriteria为准，原研究问题、原始要求、材料与历史仍保留；目标未全部完成须披露，不声称已完成被暂缓的工作。"}
		return mustJSON(v)
	}
	return contract
}
func prepareDeliveryScope(d RunDetail, c ProposeResearchRevisionCommand) (*ResearchDeliveryScope, error) {
	if c.DeliveryScope == nil {
		return nil, nil
	}
	if c.NodeID != "report_drafting" {
		return nil, fmt.Errorf("交付范围协商从报告阶段开始；不修改已执行方法或冻结路线")
	}
	var base json.RawMessage
	for _, edge := range d.Run.Compilation.Edges {
		if edge.ToNode == c.NodeID && edge.ToPort == "researchContract" {
			v, err := resolveWorkflowEdgeValue(d, edge)
			if err != nil {
				return nil, err
			}
			base = v
			break
		}
	}
	if len(base) == 0 {
		return nil, fmt.Errorf("当前报告没有可协商的研究验收记录")
	}
	before, err := researchAcceptanceCriteria(base)
	if err != nil {
		return nil, err
	}
	summary := strings.TrimSpace(c.DeliveryScope.Summary)
	if summary == "" || len([]rune(summary)) > 2000 || len(c.DeliveryScope.SuccessCriteria) < 1 || len(c.DeliveryScope.SuccessCriteria) > 30 {
		return nil, fmt.Errorf("请明确新的交付说明和1至30项验收条件")
	}
	s := &ResearchDeliveryScope{Summary: summary, PreviousCriteria: []string{}, SuccessCriteria: []string{}}
	for _, item := range before {
		s.PreviousCriteria = append(s.PreviousCriteria, item.Text)
	}
	for _, text := range c.DeliveryScope.SuccessCriteria {
		text = strings.TrimSpace(text)
		if text == "" || len([]rune(text)) > 2000 {
			return nil, fmt.Errorf("每项交付条件须为1至2000字")
		}
		s.SuccessCriteria = append(s.SuccessCriteria, text)
	}
	return s, nil
}

const researchRepairCompletionInstruction = `
本轮整改应形成可检查的实际工作，不以“将做”或重复同一局限替代。evidenceWork包含本轮保存的逐文档分析及可选workProduct，须结合原证据核对、在正文呈现相关评价，不能仅看coverage摘要而判全部缺失。researchRecordAudit是保存的来源记录→候选归并，不能误当独立试验去重或综述重叠评价。
审查提出“无法核实则披露”的替代路径前，必须明确披露能否满足本轮交付。若不能满足，不要推荐只改措辞后再跑同一路线；指出具体可核查材料/实际待做产物，或needsUserInput=true并说明需要材料还是范围选择。不因问题重复自动停止，也不把正常返修变成另一次方案整理。
limitedEvidenceAccepted只允许形成有限草稿，不自动改验收。如果researchContract.deliveryScope存在，这是用户在原返修卡确认的交付变更，应按新的successCriteria审查并诚实披露未完成原目标；其余方法、数字、来源真实性仍须检查。没有确认变更不得自行降标。只针对原始用户/已确认契约实际承诺的工作；不能把全文核验、穷尽覆盖、必须发现某人群的效应或长期效果等证据缺口自动升级成原目标。用户要的是解释证据时，有依据的不确定结论也是交付结果。
`
