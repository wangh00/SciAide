package workflow

import (
	stdcontext "context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/attachment"
	"github.com/wangh00/SciAide/internal/app/knowledge"
	"github.com/wangh00/SciAide/internal/app/tool"
)

func TestDynamicQuestionQueryMatchesLocalKnowledgeSearchLimit(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			MaxLength int `json:"maxLength"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(dynamicQuestionSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties["query"].MaxLength; got != knowledge.MaxSearchQueryRunes {
		t.Fatalf("dynamic query maxLength = %d, want %d", got, knowledge.MaxSearchQueryRunes)
	}
}

func TestDynamicRouteQuestionPromptMentionsSharedQueryLimit(t *testing.T) {
	route := starterDesignRoute()
	route.RouteID = "contract_test"
	template, _, _, err := dynamicResearchRouteTemplate(route, ResearchStarterContext{ResearchIdea: "contract test", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatalf("dynamic route template: %v", err)
	}
	if len(template.Definition.Nodes) == 0 {
		t.Fatal("dynamic route has no nodes")
	}
	question := template.Definition.Nodes[0]
	if !strings.Contains(question.Prompt, "200") || !strings.Contains(question.Prompt, "本地可信证据检索") {
		t.Fatalf("question prompt does not describe shared query contract: %q", question.Prompt)
	}
}

func TestDynamicQuestionSchemaRemainsValidJSON(t *testing.T) {
	if !json.Valid(dynamicQuestionSchema()) {
		t.Fatal("dynamic question schema is invalid JSON")
	}
}

func TestDataDeliveryReceivesActualImplementationAndResults(t *testing.T) {
	route := starterDataRoute()
	route.StageIDs = append(route.StageIDs[:len(route.StageIDs)-2], "report_drafting", "independent_review", "delivery_gate")
	route.Layers[len(route.Layers)-1].Stages = append([]ResearchRouteStage{{StageID: "report_drafting", Objective: "report", Outputs: []string{"report"}}}, route.Layers[len(route.Layers)-1].Stages...)
	template, _, _, err := dynamicResearchRouteTemplate(route, ResearchStarterContext{ResearchIdea: "analysis", StageCatalog: dynamicResearchStageCatalog()}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"report_drafting", "independent_review"} {
		for _, node := range template.Definition.Nodes {
			if node.ID == target && !strings.HasSuffix(node.PromptVersion, "-implementation-v1") {
				t.Fatalf("data delivery lost version suffix: %s", node.PromptVersion)
			}
		}
		for _, source := range []struct{ node, port, input string }{
			{"method_implementation", "analysis", "implementationContext"},
			{"python_analysis", "structured", "computedResults"},
			{"python_analysis", "artifacts", "sourceArtifacts"},
			{"question_refinement", "analysis", "researchContract"},
		} {
			found := false
			for _, edge := range template.Definition.Edges {
				found = found || edge == (Edge{FromNode: source.node, FromPort: source.port, ToNode: target, ToPort: source.input})
			}
			if !found {
				t.Errorf("missing %s -> %s.%s", source.node, target, source.input)
			}
		}
	}
}

func TestSemanticLocalExtractionDoesNotInventPublicDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		materials, discovery bool
	}{
		{"selected-full-text", true, false},
		{"no-materials-needs-discovery", false, false},
		{"explicit-discovery-with-materials", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := ResearchRoute{RouteID: "single-paper", Title: "三种运动方式的单篇中文对照与可信度", AvailableNow: true,
				StagePlans: []ResearchStagePlan{
					{StageID: "question_refinement", Objective: "明确边界"},
					{StageID: "evidence_extraction", Objective: "从指定全文提取证据"},
					{StageID: "report_drafting", Objective: "形成中文对照表"},
					{StageID: "independent_review", Objective: "独立核验"},
					{StageID: "delivery_gate", Objective: "核验交付"},
					{StageID: "report_publication", Objective: "发布报告"},
				}}
			starter := ResearchStarterContext{ResearchIdea: "比较三种运动，只用指定全文", PlannerVersion: dynamicResearchPlannerVersion, StageCatalog: dynamicResearchStageCatalog()}
			if tc.materials {
				starter.SelectedMaterials = []attachment.MessageReference{{AttachmentID: "bmj-full-text", OriginalName: "bmj-2023-075847.full.pdf"}}
			}
			if tc.discovery {
				route.StagePlans = append(route.StagePlans, ResearchStagePlan{StageID: "literature_discovery", Objective: "补充公共文献"})
			}
			projected, err := materializeSemanticResearchRoute(route, starter)
			if err != nil {
				t.Fatal(err)
			}
			wantDiscovery := !tc.materials || tc.discovery
			if slices.Contains(projected.StageIDs, "literature_discovery") != wantDiscovery {
				t.Fatalf("projected route: %v", projected.StageIDs)
			}
			for _, layer := range projected.Layers {
				for _, stage := range layer.Stages {
					if stage.Methods == nil || stage.SkillNames == nil || stage.Inputs == nil || stage.Outputs == nil {
						t.Fatalf("nullable presentation lists: %#v", stage)
					}
				}
			}
			template, _, _, err := routeDefinition(projected, starter)
			if err != nil {
				t.Fatal(err)
			}
			nodes := map[string]Node{}
			for _, node := range template.Definition.Nodes {
				nodes[node.ID] = node
			}
			for _, id := range []string{"literature_query_expansion", "literature_discovery", "candidate_screening"} {
				if _, exists := nodes[id]; exists != wantDiscovery {
					t.Fatalf("unexpected %s presence=%v", id, exists)
				}
			}
			for _, id := range []string{"candidate_review", "evidence_import", "evidence_sync", "evidence_search", "evidence_screening", "evidence_extraction", "method_selection", "independent_review", "delivery_gate"} {
				if _, exists := nodes[id]; !exists {
					t.Fatalf("missing %s", id)
				}
			}
			if tc.materials && !strings.Contains(string(nodes["candidate_review"].Arguments), "bmj-full-text") {
				t.Fatal("explicit material lost")
			}
			if tc.materials && !tc.discovery {
				if !strings.Contains(string(nodes["candidate_review"].Arguments), `"reuseSelectedMaterials":true`) {
					t.Fatal("local selection not reused")
				}
				for _, n := range nodes {
					if n.Kind == NodeAgentStage {
						for _, name := range n.AllowedTools {
							if name == "builtin.knowledge.search" || name == "builtin.web.search" || name == "builtin.web.open" {
								t.Fatal("unscoped retrieval allowed", n.ID, name)
							}
						}
					}
				}
			}
			registry := referenceTemplateRegistry(t)
			if err := registry.Register(stdcontext.Background(), fixtureTool{definition: tool.Definition{QualifiedName: "builtin.research.workflow.report", Version: "1", Description: "report fixture", Risk: tool.RiskHigh, InputSchema: raw(`{"type":"object","properties":{"name":{"type":"string"},"citations":{"type":"array"},"reportDraft":{"type":"object"},"reviewGate":{"type":"object"}}}`), OutputSchema: raw(`{"type":"object"}`)}}); err != nil {
				t.Fatal(err)
			}
			if compiled, err := NewCompiler(registry).Compile(stdcontext.Background(), template.Definition); err != nil {
				t.Fatalf("%v: %#v", err, compiled.Diagnostics)
			}
		})
	}
}

func TestDynamicRouteWithOnlySelectedMaterialsSkipsDiscoveryButKeepsEvidenceChain(t *testing.T) {
	route := ResearchRoute{
		RouteID: "local-materials", Title: "本地资料综合", AvailableNow: true,
		StageIDs:          []string{"question_refinement", "method_selection", "report_drafting", "independent_review", "delivery_gate"},
		ReviewCheckpoints: []string{"独立二次审查"},
		Layers: []ResearchRouteLayer{
			{LayerID: "question", Title: "问题", Objective: "明确研究问题", Stages: []ResearchRouteStage{{StageID: "question_refinement", Objective: "明确边界", Outputs: []string{"研究问题"}}}},
			{LayerID: "method", Title: "方法", Objective: "综合本地资料", Stages: []ResearchRouteStage{{StageID: "method_selection", Objective: "选择方法", Outputs: []string{"方法蓝图"}}, {StageID: "report_drafting", Objective: "形成资料综合", Outputs: []string{"交付稿"}}}},
			{LayerID: "delivery", Title: "交付", Objective: "独立核验", Stages: []ResearchRouteStage{{StageID: "independent_review", Objective: "独立审查", Outputs: []string{"审查结论"}}, {StageID: "delivery_gate", Objective: "核验交付", Outputs: []string{"交付许可"}}}},
		},
	}
	starterContext := ResearchStarterContext{
		ResearchIdea:      "基于已有资料形成研究综述",
		PlannerVersion:    dynamicResearchPlannerVersion,
		StageCatalog:      dynamicResearchStageCatalog(),
		SelectedMaterials: []attachment.MessageReference{{AttachmentID: "local-reference", OriginalName: "reference.pdf"}},
	}
	template, _, _, err := dynamicResearchRouteTemplate(route, starterContext, true)
	if err != nil {
		t.Fatalf("local-material route template: %v", err)
	}
	nodes := make([]string, 0, len(template.Definition.Nodes))
	for _, node := range template.Definition.Nodes {
		nodes = append(nodes, node.ID)
		if node.ID == "independent_review" {
			for _, name := range node.AllowedTools {
				if name == "builtin.knowledge.search" || name == "builtin.workspace.read_text" || name == "builtin.workspace.list" {
					t.Fatal("local review gained unscoped material access", name)
				}
			}
			if node.PromptVersion != dynamicResearchReviewVersion {
				t.Fatal("new review contract not frozen")
			}
		}
	}
	for _, absent := range []string{"literature_query_expansion", "literature_discovery", "candidate_screening"} {
		if slices.Contains(nodes, absent) {
			t.Fatalf("local-material route unexpectedly searches public literature: %v", nodes)
		}
	}
	for _, required := range []string{"candidate_review", "evidence_import", "evidence_sync", "evidence_search", "evidence_screening", "evidence_extraction"} {
		if !slices.Contains(nodes, required) {
			t.Fatalf("local-material route lost %s: %v", required, nodes)
		}
	}
	var candidate Node
	for _, node := range template.Definition.Nodes {
		if node.ID == "candidate_review" {
			candidate = node
			break
		}
	}
	var arguments struct {
		References []attachment.MessageReference `json:"referenceMaterials"`
		Candidates []any                         `json:"candidates"`
	}
	if err := json.Unmarshal(candidate.Arguments, &arguments); err != nil || len(arguments.References) != 1 || arguments.References[0].AttachmentID != "local-reference" || arguments.Candidates == nil || len(arguments.Candidates) != 0 {
		t.Fatalf("candidate selection does not preserve explicit local references: %s %v", candidate.Arguments, err)
	}
	for _, edge := range []Edge{
		{FromNode: "candidate_review", FromPort: "selectedAttachmentIds", ToNode: "evidence_import", ToPort: "selectedAttachmentIds"},
		{FromNode: "evidence_import", FromPort: "structured.attachmentIds", ToNode: "evidence_sync", ToPort: "attachmentIds"},
		{FromNode: "evidence_sync", FromPort: "structured.documentIds", ToNode: "evidence_search", ToPort: "documentIds"},
		{FromNode: "evidence_search", FromPort: "citations", ToNode: "evidence_extraction", ToPort: "candidates"},
	} {
		if !slices.Contains(template.Definition.Edges, edge) {
			t.Fatalf("local-material route lost evidence edge %#v", edge)
		}
	}
	if _, err := NewCompiler(referenceTemplateRegistry(t)).Compile(stdcontext.Background(), template.Definition); err != nil {
		t.Fatalf("local-material route does not compile: %v", err)
	}
}
