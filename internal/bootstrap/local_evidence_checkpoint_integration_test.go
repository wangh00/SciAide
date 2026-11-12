package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/tool"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

type localFollowupCheckpointConnector struct{ selectedBatchConnector }

func (c localFollowupCheckpointConnector) Search(ctx context.Context, options research.SearchOptions) ([]research.Work, error) {
	works, err := c.selectedBatchConnector.Search(ctx, options)
	if err != nil {
		return nil, err
	}
	works = works[:2]
	for i := range works {
		works[i].Abstract = fmt.Sprintf("Synthetic epsilon observation %d. This fixture tests saved local evidence, not an actual study or scientific result.", i)
	}
	return works, nil
}

// Actual HTTP adapters, knowledge tools and SQLite. A model fixture requests
// local follow-up after valid document work, is cancelled on the next request,
// then explicitly retried after restart. It must receive the saved work without
// recomputing it; the original independent review and gate remain in the chain.
func TestLocalEvidenceFollowupKeepsWorkAcrossHTTPRestartAndReview(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) {
			var syntheses, reviews, requests, documentBatches atomic.Int32
			blocked := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := int(requests.Add(1))
				body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
				if err != nil {
					t.Error(err)
					return
				}
				prompt := workflowAITestPrompt(contextPipelineCanonicalRequest(t, body))
				schema, err := workflowAITestSchema(prompt)
				if err != nil {
					t.Error(err)
					http.Error(w, "missing schema", 400)
					return
				}
				input := workflowAITestStageInput(prompt)
				properties, _ := schema["properties"].(map[string]any)
				output := workflowAISchemaFixture(schema).(map[string]any)
				if _, screening := properties["recommendedReferences"]; screening {
					n := syntheses.Add(1)
					info, _ := input["_selectedEvidence"].(map[string]any)
					ids := workflowAITestStrings(input["documentIds"])
					var refs []tool.CitationRef
					if err := json.Unmarshal(mustBootstrapJSON(t, input["candidates"]), &refs); err != nil || len(ids) != 2 || len(refs) == 0 {
						t.Errorf("fixture source inventory not visible: documents=%d citations=%d error=%v", len(ids), len(refs), err)
						http.Error(w, "source inventory", 400)
						return
					}
					if n > 1 {
						analyses, _ := info["analyses"].([]any)
						if info["direct"] == true || info["phase"] != "coverage" || len(analyses) != 2 {
							t.Errorf("local follow-up repeated completed document work: direct=%v phase=%v analyses=%d", info["direct"], info["phase"], len(analyses))
						}
						for _, raw := range analyses {
							row, _ := raw.(map[string]any)
							if row["finding"] != "Saved bounded note for "+fmt.Sprint(row["documentId"]) {
								t.Error("saved document interpretation changed")
							}
						}
					}
					if n == 2 {
						close(blocked)
						<-r.Context().Done()
						return
					}
					output["summary"] = "Synthetic local evidence only; no real-world conclusion."
					output["recommendedReferences"] = []string{refs[0].Reference}
					output["citationAssessments"] = []any{map[string]any{"reference": refs[0].Reference, "decision": "support", "sourceLevel": "abstract", "reason": "Source is a synthetic observation only", "supportingQuote": refs[0].Quote}}
					output["coverage"] = map[string]any{"sufficientForClaimedScope": true}
					output["recommendation"] = "proceed_limited"
					if n == 1 || info["direct"] == true {
						documentBatches.Add(1)
						rows := []any{}
						for _, id := range ids {
							rows = append(rows, map[string]any{"documentId": id, "finding": "Saved bounded note for " + id, "applicability": "Fixture only", "limitations": "Not real science", "references": []string{}, "workProduct": "Existing bounded fixture work for " + id})
						}
						output["documentAnalyses"] = rows
					} else {
						delete(output, "documentAnalyses") // Reuse rather than regenerate.
					}
					output["supplementalQueries"] = []string{}
					if n == 1 {
						output["supplementalQueries"] = []string{"epsilon bounded additional observation"}
					}
				} else if _, review := properties["reviewedInputSha256"]; review {
					reviews.Add(1)
					output = map[string]any{"approved": true, "reviewedInputSha256": workflowAITestStageInputSHA256(prompt), "verifiedClaims": []string{"Synthetic handoff only"}, "unsupportedClaims": []string{}, "citationIssues": []string{}, "numericIssues": []string{}, "methodIssues": []string{}, "requiredCorrections": []string{}, "confidence": "low", "limitations": []string{"A controlled fixture, not scientific acceptance"}}
				} else {
					t.Error("unexpected model stage")
					http.Error(w, "unexpected stage", 400)
					return
				}
				emitQuestionHandoffFixture(t, w, protocol, request, string(mustBootstrapJSON(t, output)), "", "")
			}))
			defer server.Close()
			options := Options{RootDir: t.TempDir(), ResearchConnectors: []research.Connector{localFollowupCheckpointConnector{}}, EventPublisher: workflowAITestPublisher{}}
			app, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			app.Startup(context.Background())
			profile, err := app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{Name: "local follow-up fixture", BaseURL: server.URL, APIProtocol: protocol, ModelID: workflowAITestModelID, Models: []modelprofile.ProfileModel{{ID: workflowAITestModelID, Enabled: true, IsDefault: true}}, TimeoutSeconds: 30, Enabled: true, IsDefault: true})
			if err != nil {
				t.Fatal(err)
			}
			project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Local follow-up isolated fixture"})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: localCheckpointWorkflow(t, app)})
			if err != nil {
				t.Fatal(err)
			}
			started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"query":"epsilon"}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
			if err != nil {
				t.Fatal(err)
			}
			waiting := driveToCandidateSelection(t, app, project.ID, started.Run.ID, 15*time.Second)
			selectionStep := currentWorkflowStep(t, waiting)
			var candidates struct{ Candidates []struct{ ID string } }
			if err := json.Unmarshal(selectionStep.Input, &candidates); err != nil || len(candidates.Candidates) != 2 {
				t.Fatal("candidate inventory", err)
			}
			selection := mustBootstrapJSON(t, map[string]any{"selectedCandidateIds": []string{candidates.Candidates[0].ID, candidates.Candidates[1].ID}})
			if _, err = app.WorkflowFacade.Decide(workflow.HumanDecisionCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: selectionStep.ID, Approved: true, Context: selection}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-blocked:
			case <-time.After(20 * time.Second):
				t.Fatal("follow-up did not reach its second HTTP request")
			}
			cancelled, err := app.WorkflowFacade.Cancel(project.ID, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var first workflow.AIExecution
			for _, e := range cancelled.AIExecutions {
				if e.Attempt == 1 && e.Status == "completed" {
					first = e
				}
			}
			if first.ID == "" || first.OutputSHA256 == "" {
				t.Fatal("validated first synthesis was not saved")
			}
			if err = app.Close(); err != nil {
				t.Fatal(err)
			}
			app, err = New(options)
			if err != nil {
				t.Fatal(err)
			}
			app.Startup(context.Background())
			app.workflow.Wait()
			reopened, err := app.WorkflowFacade.GetRun(project.ID, started.Run.ID)
			if err != nil || reopened.Run.Status != workflow.RunCancelled || syntheses.Load() != 2 {
				t.Fatal("restart automatically resumed the cancelled stage", err)
			}
			if _, err = app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: started.Run.ID, StepID: currentWorkflowStep(t, reopened).ID}); err != nil {
				t.Fatal(err)
			}
			finished := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunCompleted, 20*time.Second)
			if syntheses.Load() != 3 || reviews.Load() != 1 || requests.Load() != 4 || documentBatches.Load() != 1 {
				t.Fatalf("unexpected model work: synthesis=%d review=%d total=%d documentBatches=%d", syntheses.Load(), reviews.Load(), requests.Load(), documentBatches.Load())
			}
			firstPreserved, reviewed := false, false
			for _, e := range finished.AIExecutions {
				if e.ID == first.ID {
					firstPreserved = e.OutputSHA256 == first.OutputSHA256 && string(e.Output) == string(first.Output) && e.InputSHA256 == first.InputSHA256
				}
			}
			for _, s := range finished.Steps {
				if s.NodeID == "independent_review" {
					var in struct {
						Work struct {
							Analyses []json.RawMessage `json:"documentAnalyses"`
						} `json:"evidenceWork"`
					}
					if err := json.Unmarshal(s.Input, &in); err != nil || len(in.Work.Analyses) != 2 || s.Status != workflow.StepCompleted {
						t.Fatal("independent review did not receive the saved complete work", err)
					}
					reviewed = true
				}
				for _, old := range cancelled.Steps {
					if old.Status == workflow.StepCompleted && old.ID == s.ID && (s.Attempt != old.Attempt || string(s.Output) != string(old.Output) || s.InputSHA256 != old.InputSHA256) {
						t.Fatal("retry repeated or rewrote an upstream tool stage")
					}
				}
			}
			if !firstPreserved || !reviewed {
				t.Fatal("frozen source work or original independent review was lost")
			}
		})
	}
}

func localCheckpointWorkflow(t *testing.T, app *Application) workflow.Definition {
	t.Helper()
	base := referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition
	definition := workflow.Definition{SchemaVersion: 1, Name: "Local follow-up checkpoint fixture", Inputs: base.Inputs}
	renames := map[string]string{"import": "evidence_import", "sync": "evidence_sync", "knowledge": "evidence_search", "review": "independent_review", "review_gate": "delivery_gate"}
	keep := map[string]bool{"search": true, "select_candidates": true, "import": true, "sync": true, "knowledge": true, "review": true, "review_gate": true}
	for _, node := range base.Nodes {
		if !keep[node.ID] {
			continue
		}
		if name := renames[node.ID]; name != "" {
			node.ID = name
		}
		if node.ID == "evidence_search" {
			node.Arguments = json.RawMessage(`{"perDocument":true,"limit":20}`)
		}
		definition.Nodes = append(definition.Nodes, node)
	}
	// This is a small screening-only fixture, not a replacement research route.
	schema := json.RawMessage(`{"type":"object","required":["summary","recommendedReferences","citationAssessments","recommendation","coverage"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object"}},"recommendation":{"type":"string"},"coverage":{"type":"object"},"documentAnalyses":{"type":"array","items":{"type":"object"}},"supplementalQueries":{"type":"array","items":{"type":"string"}}}}`)
	definition.Nodes = append(definition.Nodes, workflow.Node{ID: "evidence_screening", Name: "Screen selected local evidence", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "selected-evidence-v6", Prompt: "Analyze the bounded fixture evidence only.", OutputSchema: schema})
	for _, edge := range base.Edges {
		if edge.ToNode == "review" || edge.ToNode == "review_gate" || !keep[edge.ToNode] || edge.FromNode != "$input" && !keep[edge.FromNode] {
			continue
		}
		if name := renames[edge.FromNode]; name != "" {
			edge.FromNode = name
		}
		if name := renames[edge.ToNode]; name != "" {
			edge.ToNode = name
		}
		definition.Edges = append(definition.Edges, edge)
	}
	definition.Edges = append(definition.Edges,
		workflow.Edge{FromNode: "evidence_search", FromPort: "citations", ToNode: "evidence_screening", ToPort: "candidates"},
		workflow.Edge{FromNode: "evidence_search", FromPort: "structured.documentCoverage", ToNode: "evidence_screening", ToPort: "documentCoverage"},
		workflow.Edge{FromNode: "evidence_sync", FromPort: "structured.documentIds", ToNode: "evidence_screening", ToPort: "documentIds"},
		workflow.Edge{FromNode: "evidence_import", FromPort: "structured.materials", ToNode: "evidence_screening", ToPort: "importedMaterials"},
		workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"},
		workflow.Edge{FromNode: "evidence_screening", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"},
		workflow.Edge{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	definition.Outputs = []workflow.Output{{Name: "gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}}
	return definition
}
