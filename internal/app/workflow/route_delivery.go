package workflow

import (
	"encoding/json"
	"strings"
)

// Opt-in only at route compilation; historical frozen routes retain their protocol.
const routeDeliveryVersion = "dl1"
const routeDeliveryReviewVersion = "dynamic-independent-review-v9"

const designOnlyScopeInstruction = " 本轮是无实证数据的设计交付，未采用文献证据流程。依据 originalRequest、冻结路线及研究约定完成所承诺的方案和具体材料；未设文献阶段本身不是交付缺陷，不要求补做检索筛选或调查。可按需核实方法资料，但不得伪造引用、量表效度、实测结果或伦理批准。区分本轮可以起草的内容与实施前必须核实的条件；如实披露具体限制，不用内部审计字段名替代研究内容。"
const deliveryDesignInstruction = " originalRequest 是未压缩的原始要求，routeContext 含已确认的交付与讨论，researchContract 是提炼后的研究约定；不能因提炼遗漏而丢掉原承诺。用户要求本轮给出具体草稿或材料时，在 deliverables 中按项给出 title 和完整 markdown 正文，不只写目录、占位或未来待办。该载体不限研究方向，例如问卷、访谈提纲、实验操作协议或分析规范；只生成本轮需要的材料。问卷草稿应有题干、回答选项、变量映射和适用的计分规则；自编题项标明未验证，不能冒充已验证量表；有授权限制的工具说明来源核验要求，不能擅自复刻。草稿内容可完成而验证、审批尚未完成，二者分别描述。没有额外材料时 deliverables 可省略或为空。"
const deliveryReportInstruction = " 逐项对照 originalRequest、routeContext 的确认交付及 researchContract，将上游 deliverables 的具体内容完整纳入正文或附录，保留必要题项、选项、步骤和适用边界，不能把材料重新缩成以后编制的框架；不复制整份设计 JSON。确有未完成内容应具体指出，不虚构完成。所有面向研究者的字段（含 claimSummary 和 limitations）使用研究语言，内部执行细节保留在运行记录。"
const deliveryReviewInstruction = " 对照 originalRequest、routeContext 与 researchContract 核查最终交付，不能只按问题界定的摘要自证完成。原始请求和路线是要求，不是科学证据。以本轮确认的产物成熟度验收：草稿的具体内容必须存在，但明确标注未验证不等于没起草，不能擅自要求完成实证验证或机构审批。真正缺失核心材料仍需返修；可选润色不阻断交付。"

func routeEvidenceInstruction(enabled bool, instruction string) string {
	if enabled {
		return instruction
	}
	return ""
}

func scopedDeliveryReview(node CompiledNode) bool {
	return isRouteDeliveryReview(node.PromptVersion) || strings.Contains(node.PromptVersion, "-"+routeDeliveryVersion)
}

func isRouteDeliveryReview(version string) bool {
	// Preserve the v8 delivery contract without adding question permissions to
	// its saved routes. Only newly generated v9 nodes freeze the new capability.
	return strings.HasPrefix(version, "dynamic-independent-review-v8") || strings.HasPrefix(version, routeDeliveryReviewVersion)
}

// Optional additive content carrier: existing designs still validate, while new
// routes can deliver real materials without a questionnaire-specific protocol.
func dynamicResearchDesignSchema() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(researchDesignSchema(), &schema)
	schema["properties"].(map[string]any)["deliverables"] = decodeObject(raw(`{"type":"array","maxItems":30,"items":{"type":"object","additionalProperties":false,"required":["title","markdown"],"properties":{"title":{"type":"string","minLength":1,"maxLength":240},"markdown":{"type":"string","minLength":1,"maxLength":180000}}}}`))
	return mustJSON(schema)
}
