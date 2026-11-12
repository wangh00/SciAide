package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

// Actual indexed fixture evidence, three HTTP adapters, current-Chat reissue,
// real proposal/confirmation, SQLite/restart and the original review/gate.
// No external model, formal materials, or provider configuration is used.
func TestReportOnlyInitialTextEditPreservesCitationDomainsAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testReportOnlyInitialTextEditCitations(t, protocol) })
	}
}

func testReportOnlyInitialTextEditCitations(t *testing.T, protocol modelprofile.APIProtocol) {
	var edits atomic.Int32
	var proposals atomic.Int32
	base := newWorkflowAITestServer(t, func(output map[string]any, prompt string) {
		if _, ok := output["markdown"]; ok {
			output["markdown"] = "# Draft\nPending wording " + workflowAIReissuedCitation(prompt)
			output["methodSummary"] = "Full original method metadata; preserve every character."
		}
		if _, ok := output["reviewedInputSha256"]; ok {
			applyTrackedReviewFixture(output, prompt)
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			t.Error(err)
			return
		}
		canonical := contextPipelineCanonicalRequest(t, body)
		prompt := workflowAITestPrompt(canonical)
		var request struct {
			Messages []struct{ Role string }
			Tools    []struct {
				Function struct {
					Name       string
					Parameters map[string]any
				}
			}
		}
		_ = json.Unmarshal(canonical, &request)
		for _, control := range request.Tools {
			if control.Function.Name == "research_revision_propose" {
				if proposals.Add(1) > 1 {
					emitQuestionHandoffFixture(t, w, protocol, 2, "The report-only edit is ready for explicit confirmation.", "", "")
					return
				}
				for _, m := range request.Messages {
					if m.Role == "tool" {
						emitQuestionHandoffFixture(t, w, protocol, 1, "The report-only edit is ready for explicit confirmation.", "", "")
						return
					}
				}
				command := map[string]any{"nodeId": "report_drafting", "summary": "Correct one report phrase", "changes": []string{"Replace Pending wording with Corrected wording; preserve every other author field."}, "reason": "Only the report prose changes; evidence and metadata remain unchanged."}
				emitQuestionHandoffFixture(t, w, protocol, 1, "", "research_revision_propose", string(mustBootstrapJSON(t, command)))
				return
			}
			if control.Function.Name == "submit_stage_edit" {
				reference := workflowAIReissuedCitation(prompt)
				if reference == "" {
					t.Error("current Chat citation seed missing")
					return
				}
				props := control.Function.Parameters["properties"].(map[string]any)
				hash := props["baseSHA256"].(map[string]any)["enum"].([]any)[0]
				patch := map[string]any{"baseSHA256": hash, "textEdits": []any{map[string]any{"path": "/markdown", "before": "Pending wording " + reference, "after": "Corrected wording " + reference}}}
				edits.Add(1)
				emitQuestionHandoffFixture(t, w, protocol, 2, "", "submit_stage_edit", string(mustBootstrapJSON(t, patch)))
				return
			}
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
	defer func() {
		if t.Failed() {
			rows, err := a.store.DB().Query(`SELECT error_details FROM runs WHERE status='failed' ORDER BY updated_at DESC LIMIT 2`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var detail string
					_ = rows.Scan(&detail)
					t.Log("fixture diagnostic", detail)
				}
			}
		}
		_ = a.Close()
	}()
	a.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, a, server.URL)
	profile, err = a.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{ID: profile.ID, Name: profile.Name, BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: profile.Models, TimeoutSeconds: 20, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Isolated report edit citation binding"})
	if err != nil {
		t.Fatal(err)
	}
	definition := referenceWorkflowTemplate(t, a, "trusted-research-closure").Definition
	definition.Nodes = append([]workflow.Node{}, definition.Nodes[:6]...)
	definition.Nodes[4].Arguments = json.RawMessage(`{"perDocument":true}`)
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
	definition.Edges = edges
	definition.Inputs = append(definition.Inputs, workflow.Port{Name: "contract", Type: workflow.TypeObject, Required: true})
	reviewSchema, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	definition.Nodes = append(definition.Nodes,
		workflow.Node{ID: "report_drafting", Name: "Report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6", Prompt: "Write the actual report.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["markdown","claimSummary","methodSummary","limitations","confidence"],"properties":{"markdown":{"type":"string","minLength":1},"claimSummary":{"type":"array","items":{"type":"string"}},"methodSummary":{"type":"string","minLength":1},"limitations":{"type":"array","items":{"type":"string"}},"confidence":{"type":"string","enum":["low","medium","high"]}}}`)},
		workflow.Node{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "report-edit-review-fixture-v1", Prompt: "Independently verify the current deliverable.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: reviewSchema},
		workflow.Node{ID: "delivery_gate", Name: "Original gate", Kind: workflow.NodeTool, Arguments: json.RawMessage(`{}`), ToolName: "builtin.research.workflow.review.gate"})
	for _, target := range []string{"report_drafting", "independent_review"} {
		definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "select_citations", FromPort: "citations", ToNode: target, ToPort: "evidenceContext"}, workflow.Edge{FromNode: "$input", FromPort: "contract", ToNode: target, ToPort: "researchContract"})
	}
	definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, workflow.Edge{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	definition.Outputs = []workflow.Output{{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}}
	saved, err := a.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := a.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon forty two","contract":{"successCriteria":["Provide a bounded fixture report without empirical claims"]}}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	waiting := driveToCandidateSelection(t, a, project.ID, started.Run.ID, 15*time.Second)
	step := currentWorkflowStep(t, waiting)
	var offered struct{ Candidates []struct{ ID string } }
	_ = json.Unmarshal(step.Input, &offered)
	if len(offered.Candidates) != 1 {
		t.Fatal("fixture candidate missing")
	}
	_, err = a.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: step.ID, Approved: true, Context: mustBootstrapJSON(t, map[string]any{"selectedCandidateIds": []string{offered.Candidates[0].ID}})})
	if err != nil {
		t.Fatal(err)
	}
	original := driveResearchClosure(t, a, project.ID, started.Run.ID, 30*time.Second)
	discussion, err := a.ChatFacade.StartChat(chat.StartCommand{ConversationID: original.Run.ConversationID, ModelProfileID: profile.ID, ModelID: workflowAITestModelID, Text: "Correct only Pending wording to Corrected wording in the report; preserve metadata and citations."})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		snapshot, err := a.ChatFacade.GetRunSnapshot(discussion.ID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Run.Status == chat.RunCompleted {
			break
		}
		if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
			t.Fatal("report proposal failed", snapshot.Run.ErrorMessage, snapshot.Run.ErrorDetails)
		}
	}
	pending, err := a.WorkflowFacade.GetRun(project.ID, original.Run.ID)
	if err != nil || len(pending.RevisionProposals) != 1 || !pending.RevisionProposals[0].CanConfirm {
		t.Fatal("missing report-only proposal", err)
	}
	proposal := pending.RevisionProposals[0]
	if proposal.DesignWriteIsolationVersion != "" {
		t.Fatal("fixture accidentally needs design authority")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	a.Startup(context.Background())
	if _, err := a.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: project.ID, RunID: original.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	final := waitForWorkflowState(t, a, project.ID, original.Run.ID, workflow.RunCompleted, 30*time.Second)
	if edits.Load() != 1 {
		t.Fatal("local correction regenerated the full report or added turns", edits.Load())
	}
	for i, s := range original.Steps {
		if s.NodeID == "report_drafting" {
			break
		}
		if !reflect.DeepEqual(s, final.Steps[i]) {
			t.Fatal("unchanged evidence reran", s.NodeID)
		}
	}
	var report workflow.Step
	for _, s := range final.Steps {
		if s.NodeID == "report_drafting" {
			report = s
		}
	}
	var source struct {
		Analysis struct{ Markdown, MethodSummary string }
	}
	_ = json.Unmarshal(report.Output, &source)
	if !strings.Contains(source.Analysis.Markdown, "Corrected wording") || source.Analysis.MethodSummary != "Full original method metadata; preserve every character." {
		t.Fatal("local edit lost metadata")
	}
	var chatRunID, reference, quote, draft string
	err = a.store.DB().QueryRow(`SELECT b.chat_run_id,c.reference_key,c.quote_text FROM workflow_ai_executions e JOIN workflow_ai_chat_runs b ON b.execution_id=e.id JOIN message_citations c ON c.run_id=b.chat_run_id WHERE e.workflow_step_id=? AND e.attempt=?`, report.ID, report.Attempt).Scan(&chatRunID, &reference, &quote)
	if err != nil || reference == "" || !strings.Contains(quote, "epsilon forty two") || strings.Contains(source.Analysis.Markdown, reference) {
		t.Fatal("Chat/Workflow citation domains were not preserved", err)
	}
	if err := a.store.DB().QueryRow(`SELECT draft_text FROM model_turn_journal WHERE run_id=? ORDER BY turn_index DESC LIMIT 1`, chatRunID).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft, "textEdits") || !strings.Contains(draft, reference) || !strings.Contains(draft, "Pending wording") {
		t.Fatal("original edit payload not audited")
	}
	if final.DeliveryAssessment == nil || final.DeliveryAssessment.Status != "reviewed" {
		t.Fatal("original scientific gate did not run")
	}
	t.Log(fmt.Sprintf("one explicit edit, retained metadata, current Chat citation bound; %s", protocol))
}
