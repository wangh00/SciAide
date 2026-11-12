package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const designMaterialsHeading = "## 附录：本轮设计材料"
const designMaterialAssemblyVersion = "design-material-assembly-v2"
const designMaterialPromptSuffix = "-dm2"
const designMaterialDeliveryInstruction = "逐项核对本轮承诺交付，在当前阶段给出实际材料而不是以后编制的待办。设计阶段 deliverables 可承载完整材料：保留具体题项、选项、变量编码、操作步骤或交接内容，只按本轮要求生成，不固定学科。未知量表参数、授权或实施条件如实标为待核实，不妨碍起草允许自编的内容，也不得伪造验证。报告阶段从上游设计承接材料，不把已经存在的正文缩成待办。宿主将尚未在报告完整出现的材料原文附在“附录：本轮设计材料”，无需再抄写该附录；正文仍须准确概括，且不得与附录冲突。"
const designMaterialAssemblyInstruction = "本报告使用分离的设计材料组装：宿主会把上游 deliverables 的全部非空原文单独附入设计材料附录，不以正文是否出现相同文字判断去重。报告只写本轮需要的科学正文与准确概括，不再抄写这些完整材料或宿主附录；偶然相同短语不代表材料归属。材料缺内容或需替换须回其设计产出阶段，不能以相矛盾的正文副本代改。原文承接不代表材料已经验证或科学审查通过。"

// Carry actual upstream bodies, never synthesize missing scientific material.
// The original model response and normalization hashes remain in the audit.
func frozenDesignContext(input json.RawMessage) (string, map[string]any) {
	stage := decodeObject(input)
	design, _ := stage["designContext"].(map[string]any)
	if design != nil {
		return "designContext", design
	}
	design, _ = stage["context"].(map[string]any)
	return "context", design
}

func enrichDesignMaterials(value, input json.RawMessage) (json.RawMessage, []AIStageNormalization, error) {
	if usesDesignMaterialAssembly(input) {
		// New compiled reports assemble a separate, verifiable managed section;
		// an incidental substring in authored prose is not a material handoff.
		return value, nil, nil
	}
	_, design := frozenDesignContext(input)
	if design["status"] != "research_design_not_empirical_result" {
		return value, nil, nil
	}
	var materials []struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	if json.Unmarshal(mustJSON(design["deliverables"]), &materials) != nil || len(materials) == 0 {
		return value, nil, nil
	}
	fields := decodeObject(value)
	markdown, ok := fields["markdown"].(string)
	if !ok {
		return value, nil, fmt.Errorf("报告缺少 Markdown 正文")
	}
	original := markdown
	sections := []string{}
	for _, material := range materials {
		body := strings.TrimSpace(material.Markdown)
		if body == "" || strings.Contains(original, body) {
			continue
		}
		sections = append(sections, "### "+strings.Join(strings.Fields(material.Title), " ")+"\n\n"+body)
	}
	if len(sections) == 0 {
		return value, nil, nil
	}
	if strings.Contains(original, designMaterialsHeading) {
		return nil, nil, fmt.Errorf("设计材料附录与上游正文不一致；请省略宿主附录，材料修正应回到研究设计阶段，不能静默改写")
	}
	appendix := designMaterialsHeading + "\n\n" + strings.Join(sections, "\n\n")
	// Keep the retrieval-log appendix last, including idempotent precommit/final passes.
	if i := strings.Index(markdown, retrievalAppendixHeading); i >= 0 {
		markdown = strings.TrimSpace(markdown[:i]) + "\n\n" + appendix + "\n\n" + markdown[i:]
	} else {
		markdown = strings.TrimSpace(markdown) + "\n\n" + appendix
	}
	fields["markdown"] = markdown
	return mustJSON(fields), []AIStageNormalization{{Path: "$.markdown", Rule: "append_frozen_design_materials", BeforeSHA256: hashBytes([]byte(original)), AfterSHA256: hashBytes([]byte(markdown))}}, nil
}

func usesDesignMaterialAssembly(input json.RawMessage) bool {
	return decodeObject(input)["designMaterialAssembly"] == designMaterialAssemblyVersion
}

func reportDesignMaterialAppendix(input json.RawMessage) string {
	if !usesDesignMaterialAssembly(input) {
		return ""
	}
	_, design := frozenDesignContext(input)
	if design["status"] != "research_design_not_empirical_result" {
		return ""
	}
	var materials []struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	if json.Unmarshal(mustJSON(design["deliverables"]), &materials) != nil {
		return ""
	}
	sections := []string{}
	for _, material := range materials {
		body := strings.TrimSpace(material.Markdown)
		if body != "" {
			sections = append(sections, "### "+strings.Join(strings.Fields(material.Title), " ")+"\n\n"+body)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return designMaterialsHeading + "\n\n" + strings.Join(sections, "\n\n")
}
