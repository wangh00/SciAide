package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestPlanningQuestionsAreOptionalDirectionDecisions(t *testing.T) {
	for _, tc := range []struct {
		name, kind, label string
		valid             bool
	}{
		{"direction", "research_direction", "比较组间差异", true},
		{"resource kind", "resource_upload", "选择文件", false},
		{"missing kind", "", "比较组间差异", false},
		{"upload command", "research_direction", "立即上传数据文件（CSV/TSV/XLSX）", false},
		{"deferred upload", "research_direction", "稍后再上传，先查看方案", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := plannerRegressionPayload(t, false)
			payload["clarification"] = map[string]any{"needsUserInput": true, "questions": []any{map[string]any{
				"id": "goal", "kind": tc.kind, "text": "主要研究目标是什么？", "impact": "决定研究假设与分析方向", "required": true, "selectionMode": "single",
				"options": []any{map[string]any{"id": "compare", "label": tc.label}, map[string]any{"id": "predict", "label": "预测生长指标"}},
			}}}
			encoded, _ := json.Marshal(payload)
			node := CompiledNode{ID: "explore", OutputSchema: semanticResearchStarterSchema()}
			err := (tool.JSONSchemaValidator{}).Validate(node.OutputSchema, encoded)
			if err == nil {
				err = validateWorkflowAIStageOutput(node, encoded, raw(`{}`))
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
	for _, clarification := range []any{nil, map[string]any{"needsUserInput": false, "questions": []any{}}} {
		payload := plannerRegressionPayload(t, false)
		if clarification != nil {
			payload["clarification"] = clarification
		}
		encoded, _ := json.Marshal(payload)
		if err := (tool.JSONSchemaValidator{}).Validate(semanticResearchStarterSchema(), encoded); err != nil {
			t.Fatal(err)
		}
	}
	prompt := ResearchStarterTemplate().Definition.Nodes[0].Prompt
	for _, rule := range []string{"信息充分时省略 clarification", "先由用户采纳路线", "禁止作为 clarification", "其他目录"} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("missing planning rule: %s", rule)
		}
	}
}
