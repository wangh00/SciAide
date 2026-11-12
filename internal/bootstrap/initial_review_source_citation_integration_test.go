package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestInitialReviewSourcesPreserveCitationDomainsAndRestartAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testInitialReviewSourceCitations(t, protocol) })
	}
}

func testInitialReviewSourceCitations(t *testing.T, protocol modelprofile.APIProtocol) {
	var reviews atomic.Int32
	var rawSelector, workflowQuote, chatReference string
	base := newWorkflowAITestServer(t, func(output map[string]any, prompt string) {
		if _, ok := output["markdown"]; ok {
			output["markdown"] = "Current exact report 誊写 " + workflowAIReissuedCitation(prompt)
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
		if strings.Contains(prompt, "<review_submission_sources>") {
			reviews.Add(1)
			input := workflowAITestStageInput(prompt)
			const open, close = "<review_submission_sources>", "</review_submission_sources>"
			start := strings.Index(prompt, open) + len(open)
			end := strings.Index(prompt[start:], close)
			var menu struct {
				CitationAliases map[string]string
			}
			if end < 0 || json.Unmarshal([]byte(prompt[start:start+end]), &menu) != nil {
				t.Error("bad current menu")
				return
			}
			pairs := []string{}
			for current, old := range menu.CitationAliases {
				pairs = append(pairs, current, old)
			}
			workflowQuote = strings.NewReplacer(pairs...).Replace(input["context"].(map[string]any)["markdown"].(string))
			chatReference = workflowAIReissuedCitation(prompt)
			if !strings.HasPrefix(workflowQuote, "Current exact report 誊写 [K-") || chatReference == "" || strings.Contains(workflowQuote, chatReference) {
				t.Error("not distinct current Chat / frozen Workflow domains")
				return
			}
			checks := []any{}
			for _, c := range input["acceptanceCriteria"].([]any) {
				checks = append(checks, map[string]any{"criterionId": c.(map[string]any)["id"], "status": "not_met", "basis": "The scripted report needs an independent evidence assessment"})
			}
			id := input["reviewHistory"].(map[string]any)["issuePrefix"].(string) + "1"
			finding := workflowAITestTrackedFinding(id, "open", "methodIssues", "Evidence assessment remains unverified", map[string]any{"pointer": "/context/markdown", "quote": workflowQuote}, "new", "First detection from the current report", 0)
			delete(finding, "correctionIndex")
			finding["correction"] = "Independently verify the report evidence assessment"
			revision := map[string]any{"nodeId": "report_drafting", "reason": "Correct the report evidence assessment", "whyNotLater": "The report owns this claim", "needsUserInput": false, "requiredInput": ""}
			if contract, ok := input["revisionWriteContract"].(map[string]any); ok {
				for _, raw := range contract["objects"].([]any) {
					v := raw.(map[string]any)
					if v["ownerNodeId"] == "report_drafting" && (v["kind"] == "stage_result" || v["kind"] == "report_authored_body") {
						revision["editTargets"] = []any{v["id"]}
						break
					}
				}
			}
			finding["revision"] = revision
			output := map[string]any{"approved": false, "verifiedClaims": []string{}, "confidence": "low", "limitations": []string{}, "suggestions": []string{}, "acceptanceChecks": checks, "reviewFindings": []any{finding}}
			rawSelector = string(reviewFixtureSourceSelections(t, prompt, output))
			emitQuestionHandoffFixture(t, w, protocol, int(reviews.Load()), "", "submit_stage_result", rawSelector)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}, ResearchConnectors: []research.Connector{researchClosureConnector{}}}
	a, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	a.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, a, server.URL)
	profile, err = a.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{ID: profile.ID, Name: profile.Name, BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: profile.Models, TimeoutSeconds: 10, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "First source review citation isolation"})
	if err != nil {
		t.Fatal(err)
	}
	d := referenceWorkflowTemplate(t, a, "trusted-research-closure").Definition
	d.Nodes = append([]workflow.Node{}, d.Nodes[:6]...)
	d.Nodes[4].Arguments = json.RawMessage(`{"perDocument":true}`)
	kept := map[string]bool{"$input": true}
	for _, n := range d.Nodes {
		kept[n.ID] = true
	}
	edges := []workflow.Edge{}
	for _, e := range d.Edges {
		if kept[e.FromNode] && kept[e.ToNode] {
			edges = append(edges, e)
		}
	}
	d.Edges = edges
	d.Inputs = append(d.Inputs, workflow.Port{Name: "contract", Type: workflow.TypeObject, Required: true})
	schema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	d.Nodes = append(d.Nodes,
		workflow.Node{ID: "report_drafting", Name: "Report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Write the actual report.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["markdown","claimSummary","methodSummary","limitations","confidence"],"properties":{"markdown":{"type":"string","minLength":1},"claimSummary":{"type":"array","items":{"type":"string"}},"methodSummary":{"type":"string","minLength":1},"limitations":{"type":"array","items":{"type":"string"}},"confidence":{"type":"string","enum":["low","medium","high"]}}}`)},
		workflow.Node{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-independent-review-v9-focused-v1", Prompt: "Independently assess current evidence.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: schema},
		workflow.Node{ID: "delivery_gate", Name: "Original gate", Kind: workflow.NodeTool, Arguments: json.RawMessage(`{}`), ToolName: "builtin.research.workflow.review.gate"})
	for _, target := range []string{"report_drafting", "independent_review"} {
		d.Edges = append(d.Edges, workflow.Edge{FromNode: "select_citations", FromPort: "citations", ToNode: target, ToPort: "evidenceContext"}, workflow.Edge{FromNode: "$input", FromPort: "contract", ToNode: target, ToPort: "researchContract"})
	}
	d.Edges = append(d.Edges, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, workflow.Edge{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	d.Outputs = []workflow.Output{{Name: "review", Type: workflow.TypeObject, FromNode: "independent_review", FromPort: "analysis", Required: true}}
	saved, err := a.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: d})
	if err != nil {
		t.Fatal(err)
	}
	started, err := a.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon forty two","contract":{"successCriteria":["Provide a bounded fixture report"]}}`), PermissionMode: conversation.PermissionFullAccess, ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := driveToCandidateSelection(t, a, project.ID, started.Run.ID, 15*time.Second)
	step := currentWorkflowStep(t, waiting)
	var offered struct{ Candidates []struct{ ID string } }
	_ = json.Unmarshal(step.Input, &offered)
	if len(offered.Candidates) != 1 {
		t.Fatal("fixture citation candidate missing")
	}
	if _, err := a.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: mustBootstrapJSON(t, map[string]any{"selectedCandidateIds": []string{offered.Candidates[0].ID}})}); err != nil {
		t.Fatal(err)
	}
	citationWaiting := waitForWorkflowState(t, a, project.ID, started.Run.ID, workflow.RunWaitingHumanConfirmation, 15*time.Second)
	citationStep := currentWorkflowStep(t, citationWaiting)
	var citationInput struct{ Candidates []tool.CitationRef }
	if citationStep.NodeKind != workflow.NodeCitationSelection || json.Unmarshal(citationStep.Input, &citationInput) != nil || len(citationInput.Candidates) == 0 {
		t.Fatal("expected explicit fixture citation choice")
	}
	if _, err := a.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: citationStep.ID, Approved: true, Context: mustBootstrapJSON(t, []tool.CitationRef{citationInput.Candidates[0]})}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, a, project.ID, started.Run.ID, workflow.RunFailed, 25*time.Second)
	if final.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || reviews.Load() != 1 {
		t.Fatal("source/citation conversion changed science or repeated submission", final.Run.ErrorCode, reviews.Load())
	}
	var execution workflow.AIExecution
	for _, e := range final.AIExecutions {
		if workflow.IsReviewSourceProtocol(e.PromptVersion) {
			execution = e
		}
	}
	var reviewOutput struct {
		ReviewFindings []struct {
			Basis []struct{ Pointer, Quote string }
		}
	}
	_ = json.Unmarshal(execution.Output, &reviewOutput)
	if execution.Status != "completed" || len(reviewOutput.ReviewFindings) != 1 || len(reviewOutput.ReviewFindings[0].Basis) != 1 || reviewOutput.ReviewFindings[0].Basis[0].Quote != workflowQuote || strings.Contains(string(execution.Output), chatReference) {
		t.Fatalf("workflow output lost frozen citation domain: status=%s version=%s expected=%q chat=%s output=%s", execution.Status, execution.PromptVersion, workflowQuote, chatReference, execution.Output)
	}
	var message, draft, reference, quote string
	if err := a.store.DB().QueryRow(`SELECT p.text_content FROM message_parts p JOIN runs r ON r.assistant_message_id=p.message_id WHERE r.id=? AND p.part_type='text' ORDER BY p.ordinal LIMIT 1`, execution.ChatRunID).Scan(&message); err != nil || !strings.Contains(message, chatReference) {
		t.Fatal("completed Chat quote not current-issued", err)
	}
	if err := a.store.DB().QueryRow(`SELECT reference_key,quote_text FROM message_citations WHERE run_id=?`, execution.ChatRunID).Scan(&reference, &quote); err != nil || reference != chatReference || !strings.Contains(quote, "epsilon forty two") {
		t.Fatal("derived Chat quote has no matching persisted citation", err)
	}
	if err := a.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? AND turn_index=1`, execution.ChatRunID).Scan(&draft); err != nil || draft != rawSelector || strings.Contains(draft, `"quote"`) {
		t.Fatal("source selector audit rewritten", err)
	}
	before := string(mustBootstrapJSON(t, final.Steps))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	a.Startup(context.Background())
	reopened, err := a.WorkflowFacade.GetRun(project.ID, final.Run.ID)
	if err != nil || reviews.Load() != 1 || before != string(mustBootstrapJSON(t, reopened.Steps)) {
		t.Fatal("restart changed history or called review again", err)
	}
}
