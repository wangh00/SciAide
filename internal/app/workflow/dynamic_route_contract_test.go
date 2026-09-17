package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangh00/SciAide/internal/app/knowledge"
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
