package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

const revisionWriteVersion = "revision-write-objects-v1"

// Evidence and edit destinations are deliberately separate. Citing a correct
// design to correct report wording does not require editing that design.
type revisionWriteObject struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Label            string   `json:"label"`
	OwnerNodeID      string   `json:"ownerNodeId"`
	StepID           string   `json:"stepId"`
	Attempt          int      `json:"attempt"`
	Ordinal          int      `json:"ordinal"`
	OutputSHA256     string   `json:"outputSha256"`
	OutputPointer    string   `json:"outputPointer"`
	ReviewRoots      []string `json:"reviewRoots,omitempty"`
	ReportCopyPolicy string   `json:"reportCopyPolicy,omitempty"`
}

type revisionWriteContract struct {
	Version string                `json:"version"`
	Objects []revisionWriteObject `json:"objects"`
}

func supportsRevisionWriteObjects(schema json.RawMessage) bool {
	properties, _ := decodeObject(schema)["properties"].(map[string]any)
	plan, _ := properties["revisionPlan"].(map[string]any)
	item, _ := plan["items"].(map[string]any)
	fields, _ := item["properties"].(map[string]any)
	return fields["editTargets"] != nil
}

func pointerSegment(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

// Derive ownership from the actual report input and graph, never a title,
// quote, finding, or model-supplied attribution. Nil means this is not a frozen
// design-material route. Errors are host binding failures, not model mistakes.
func currentRevisionWriteContract(d RunDetail, reviewID string, input json.RawMessage, targets []ResearchRevisionTarget) (*revisionWriteContract, error) {
	assembled, ok := reviewAssembledInput(input)
	if !ok {
		return nil, fmt.Errorf("返修写入对象无法还原当前报告")
	}
	fields := decodeObject(assembled)
	nodes := compilationNodeMap(d.Run.Compilation)
	var report *Step
	var design *Step
	var materialValues []map[string]any
	for _, edge := range d.Run.Compilation.Edges {
		if edge.ToNode != reviewID || edge.FromPort != "analysis" || !usesReportProvenance(nodes[edge.FromNode]) {
			continue
		}
		candidate := findStepByNode(d.Steps, edge.FromNode)
		if candidate == nil || candidate.Status != StepCompleted {
			continue
		}
		key, context := frozenDesignContext(candidate.Input)
		if context["status"] != "research_design_not_empirical_result" {
			continue
		}
		var materials []map[string]any
		if value, exists := context["deliverables"]; exists && json.Unmarshal(mustJSON(value), &materials) != nil {
			return nil, fmt.Errorf("返修设计材料集合无法读取")
		}
		if report != nil || candidate.InputSHA256 == "" || hashJSON(candidate.Input) != candidate.InputSHA256 {
			return nil, fmt.Errorf("返修设计材料的报告来源不唯一或冻结输入不一致")
		}
		value, err := resolveWorkflowEdgeValue(d, edge)
		if err != nil || !rawJSONEqual(value, mustJSON(fields[edge.ToPort])) {
			return nil, fmt.Errorf("返修设计材料未绑定当前报告结果")
		}
		for _, source := range d.Run.Compilation.Edges {
			if source.ToNode != candidate.NodeID || source.ToPort != key || source.FromNode == "$input" || source.FromPort != "analysis" {
				continue
			}
			owner := findStepByNode(d.Steps, source.FromNode)
			actual, err := resolveWorkflowEdgeValue(d, source)
			if design != nil || owner == nil || err != nil || !rawJSONEqual(actual, mustJSON(context)) {
				return nil, fmt.Errorf("返修设计材料与其产出阶段不一致")
			}
			design = owner
		}
		if design == nil {
			return nil, fmt.Errorf("返修设计材料缺少可核对的产出阶段")
		}
		report, materialValues = candidate, materials
	}
	if report == nil {
		return nil, nil
	}
	contract := &revisionWriteContract{Version: revisionWriteVersion, Objects: []revisionWriteObject{}}
	add := func(step Step, kind, label, port, path string) *revisionWriteObject {
		object := revisionWriteObject{Kind: kind, Label: label, OwnerNodeID: step.NodeID, StepID: step.ID, Attempt: step.Attempt, Ordinal: step.Ordinal, OutputSHA256: hashJSON(step.Output), OutputPointer: path}
		object.ID = "edit-" + hashJSON(mustJSON(object))
		for _, edge := range d.Run.Compilation.Edges {
			if edge.FromNode == step.NodeID && edge.ToNode == reviewID && edge.FromPort == port {
				object.ReviewRoots = append(object.ReviewRoots, "/"+pointerSegment(edge.ToPort)+strings.TrimPrefix(path, "/"+pointerSegment(port)))
			}
		}
		contract.Objects = append(contract.Objects, object)
		return &contract.Objects[len(contract.Objects)-1]
	}
	for _, target := range targets {
		step := findStepByNode(d.Steps, target.NodeID)
		if step == nil || step.Status != StepCompleted {
			return nil, fmt.Errorf("返修写入对象的阶段尚未完成")
		}
		port := "analysis"
		if nodes[step.NodeID].Kind == NodeCitationSelection {
			port = "citations"
		}
		if _, err := outputPort(step.Output, port); err != nil {
			return nil, fmt.Errorf("返修写入对象缺少实际输出端口 %s.%s", step.NodeID, port)
		}
		kind, path := "stage_result", "/"+pointerSegment(port)
		if step.ID == report.ID {
			kind, path = "report_authored_body", "/analysis/markdown"
		}
		add(*step, kind, target.Label, port, path)
	}
	// Collection-level creation is explicit, even if deliverables was omitted
	// or empty. It is not an invented existing scalar or a report-owned draft.
	if schemaDeclaresProperty(nodes[design.NodeID].OutputSchema, "deliverables") || len(materialValues) > 0 {
		add(*design, "design_material_collection", "设计材料集合（可新增条目）", "analysis", "/analysis/deliverables")
	}
	for i, material := range materialValues {
		object := add(*design, "frozen_design_material", fmt.Sprint(material["title"]), "analysis", fmt.Sprintf("/analysis/deliverables/%d/markdown", i))
		object.ReportCopyPolicy = "legacy_verbatim_material"
		if usesDesignMaterialAssembly(report.Input) {
			object.ReportCopyPolicy = "host_design_appendix"
		}
	}
	return contract, nil
}

func validateRevisionWriteObjects(plan []researchRevisionPlanItem, input json.RawMessage) error {
	var stage struct {
		Contract *revisionWriteContract   `json:"revisionWriteContract"`
		Targets  []ResearchRevisionTarget `json:"revisionTargets"`
	}
	if err := json.Unmarshal(input, &stage); err != nil {
		return err
	}
	if stage.Contract == nil {
		for _, item := range plan {
			if len(item.EditTargets) > 0 {
				return fmt.Errorf("当前冻结审查没有 revisionWriteContract，不能使用未提供的 editTargets；请省略该字段")
			}
		}
		return nil
	} // historical frozen input
	if stage.Contract.Version != revisionWriteVersion || len(stage.Contract.Objects) == 0 {
		return fmt.Errorf("返修写入对象契约版本或内容无效")
	}
	objects, allowed := map[string]revisionWriteObject{}, map[string]bool{}
	for _, target := range stage.Targets {
		allowed[target.NodeID] = true
	}
	for _, object := range stage.Contract.Objects {
		if object.ID == "" || objects[object.ID].ID != "" {
			return fmt.Errorf("返修写入对象身份不唯一")
		}
		objects[object.ID] = object
	}
	// Use actual frozen ordinals, not array order or a stage's friendly name.
	for i, item := range plan {
		if len(item.EditTargets) == 0 && !item.NeedsUserInput {
			return fmt.Errorf("revisionPlan/%d/editTargets 缺少实际写入对象；事实依据不等于编辑目标", i)
		}
		seen := map[string]bool{}
		var earliest *revisionWriteObject
		for _, id := range item.EditTargets {
			object, exists := objects[id]
			if !exists || seen[id] {
				return fmt.Errorf("revisionPlan/%d/editTargets 包含未知、过期或重复写入对象 %q", i, id)
			}
			seen[id] = true
			if !allowed[object.OwnerNodeID] {
				return fmt.Errorf("revisionPlan/%d/editTargets 的产出阶段 %q 不可返修；不能指定报告代改冻结材料", i, object.OwnerNodeID)
			}
			if earliest == nil || object.Ordinal < earliest.Ordinal {
				copy := object
				earliest = &copy
			}
		}
		owner := ""
		if earliest != nil {
			owner = earliest.OwnerNodeID
		}
		if owner != "" && item.NodeID != owner && !(item.NodeID == "" && item.NeedsUserInput) {
			return fmt.Errorf("revisionPlan/%d/nodeId=%q 不能覆盖选定写入对象；其最小返修起点为 %q。报告正文不能改写冻结设计材料；请明确修正写入对象或阶段，不要删除真实缺陷", i, item.NodeID, owner)
		}
	}
	return nil
}

const revisionWriteInstruction = `
Write ownership: stage_input.revisionWriteContract lists editable objects and their frozen producer identity. For each open correction, revision.editTargets (canonical revisionPlan[].editTargets) must list the object IDs you intend to CHANGE, not the objects you merely cite as evidence. Select nodeId as the earliest owner of those writes in revisionTargets; the host verifies this declared binding, NOT the scientific correctness of your selection or intent in prose. To replace, complete or correct a frozen_design_material, select that material's ID and producer, NOT report_drafting: report assembly preserves upstream text verbatim. For missing materials select design_material_collection (creation is allowed); that pointer may not yet exist. host_design_appendix is a separately assembled section, identified by reportComposition when present; legacy_verbatim_material is only a carry-forward rule, NOT proof that an arbitrary matching substring is copied or read-only. report_authored_body covers authored narrative, not permission to change upstream material via a contradictory report copy. A report-only wording/summary omission can select that body even when its factual basis quotes a correct design. Changing a reusable stage_result requires that upstream object. Keep all scientific checks and unresolved inputs honest. These write IDs are navigation, never scientific evidence or approval.
`
