package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestEvidenceScreeningBindsChatCitationsBeforeSelection(t *testing.T) {
	server := newResearchStarterOutputTestServer(t, func(output map[string]any, prompt string) {
		input := workflowAITestStageInput(prompt)
		candidates, _ := input["candidates"].([]any)
		if len(candidates) == 0 {
			return
		}
		refs, assessments := []string{}, []any{}
		for _, item := range candidates {
			ref := item.(map[string]any)["reference"].(string)
			refs = append(refs, ref)
			assessments = append(assessments, map[string]any{"reference": ref, "decision": "include"})
		}
		output["summary"] = "Verified finding " + refs[0]
		output["recommendedReferences"] = refs
		output["citationAssessments"] = assessments
		output["recommendation"] = "proceed_limited"
	})
	a, err := New(Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{researchClosureConnector{}}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, a, server.URL)
	project, err := a.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Evidence citation binding"})
	if err != nil {
		t.Fatal(err)
	}
	template := referenceWorkflowTemplate(t, a, "trusted-research-closure")
	definition := template.Definition
	definition.Name = "Evidence citation binding"
	definition.Nodes = append([]workflow.Node{}, definition.Nodes[:6]...)
	definition.Nodes[4].Arguments = json.RawMessage(`{"perDocument":true}`)
	definition.Nodes = append(definition.Nodes, workflow.Node{ID: "evidence_screening", Name: "Screen evidence", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "binding-test-v1", Prompt: "Evaluate candidate excerpts.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["summary","recommendedReferences","citationAssessments","recommendation"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object"}},"recommendation":{"type":"string"}}}`)})
	kept := map[string]bool{"$input": true}
	for _, n := range definition.Nodes {
		kept[n.ID] = true
	}
	edges := []workflow.Edge{}
	for _, e := range definition.Edges {
		if kept[e.FromNode] && kept[e.ToNode] {
			edges = append(edges, e)
		}
	}
	definition.Edges = append(edges,
		workflow.Edge{FromNode: "knowledge", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"},
		workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "select_citations", ToPort: "screening"})
	definition.Outputs = []workflow.Output{{Name: "citations", Type: workflow.TypeCitations, FromNode: "select_citations", FromPort: "citations", Required: true}}
	saved, err := a.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := a.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon forty two"}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := driveToCandidateSelection(t, a, project.ID, started.Run.ID, 15*time.Second)
	step := currentWorkflowStep(t, waiting)
	var input struct {
		Candidates []struct {
			ID string `json:"id"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(step.Input, &input); err != nil || len(input.Candidates) == 0 {
		t.Fatal("missing candidates", err)
	}
	selection, _ := json.Marshal(map[string]any{"selectedCandidateIds": []string{input.Candidates[0].ID}})
	if _, err := a.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: selection}); err != nil {
		t.Fatal(err)
	}
	evidence := waitForWorkflowState(t, a, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 20*time.Second)
	var screen workflow.AIExecution
	for _, e := range evidence.AIExecutions {
		if e.PromptVersion == "binding-test-v1" {
			screen = e
		}
	}
	if screen.Status != "completed" || screen.ChatRunID == "" {
		t.Fatalf("screening failed: %+v", screen)
	}
	var ref, quote string
	if err := a.store.DB().QueryRow(`SELECT reference_key,quote_text FROM message_citations WHERE run_id=?`, screen.ChatRunID).Scan(&ref, &quote); err != nil {
		t.Fatal("screening citation not bound", err)
	}
	if !strings.Contains(screen.OutputText, ref) || strings.Contains(string(screen.Output), ref) || !strings.Contains(quote, "epsilon forty two") {
		t.Fatal("citation did not cross Chat/Workflow boundary correctly")
	}
	var selectedInput struct {
		Candidates []tool.CitationRef `json:"candidates"`
	}
	if err := json.Unmarshal(currentWorkflowStep(t, evidence).Input, &selectedInput); err != nil || len(selectedInput.Candidates) == 0 {
		t.Fatal(err)
	}
	if !strings.Contains(string(screen.Output), selectedInput.Candidates[0].Reference) {
		t.Fatal("screening output no longer matches selection")
	}
}
