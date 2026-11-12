package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	wailstransport "github.com/wangh00/SciAide/internal/transport/wails"
)

func TestMaterialOwnershipRevisionAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, materialDefect := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/material_%t", protocol, materialDefect), func(t *testing.T) { testMaterialOwnershipRevision(t, protocol, materialDefect) })
		}
	}
}

func testMaterialOwnershipRevision(t *testing.T, protocol modelprofile.APIProtocol, materialDefect bool, discussion ...bool) {
	testMaterialOwnershipRevisionWithEdits(t, protocol, materialDefect, false, discussion...)
}

func TestInitialRevisionTextEditAndReportReuseAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testMaterialOwnershipRevisionWithEdits(t, protocol, true, true) })
	}
}

func testMaterialOwnershipRevisionWithEdits(t *testing.T, protocol modelprofile.APIProtocol, materialDefect, textEdit bool, discussion ...bool) {
	testMaterialOwnershipRevisionWithControls(t, protocol, materialDefect, textEdit, false, discussion...)
}

func TestInitialRevisionCharacterCopyAndReportReuseAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		t.Run(string(protocol), func(t *testing.T) { testMaterialOwnershipRevisionWithControls(t, protocol, true, true, true) })
	}
}

func testMaterialOwnershipRevisionWithControls(t *testing.T, protocol modelprofile.APIProtocol, materialDefect, textEdit, characterCopy bool, discussion ...bool) {
	var probe *discussionOwnershipProbe
	if len(discussion) > 0 && discussion[0] {
		probe = &discussionOwnershipProbe{protocol: protocol, material: materialDefect}
	}
	var mu sync.Mutex
	methods, designs, reports, reviews, repairs := 0, 0, 0, 0, 0
	var rejectedWire map[string]any
	var frozenReviewSchema json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			t.Error(err)
			return
		}
		canonical := contextPipelineCanonicalRequest(t, body)
		if probe != nil && probe.respond(t, w, body, canonical) {
			return
		}
		prompt := workflowAITestPrompt(canonical)
		input := workflowAITestStageInput(prompt)
		if probe != nil {
			probe.checkStage(t, input)
		}
		schema, err := workflowAITestSchema(prompt)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var request struct {
			Tools []struct {
				Function struct {
					Name       string
					Parameters map[string]any
				}
			}
		}
		_ = json.Unmarshal(canonical, &request)
		for _, control := range request.Tools {
			if control.Function.Name != "submit_stage_repair" {
				continue
			}
			if rejectedWire == nil {
				t.Error("unexpected repair")
				return
			}
			props := control.Function.Parameters["properties"].(map[string]any)
			base := props["baseSHA256"].(map[string]any)["enum"].([]any)[0]
			f := rejectedWire["reviewFindings"].([]any)[0].(map[string]any)
			f["revision"].(map[string]any)["nodeId"] = "research_design"
			repairs++
			patch := mustBootstrapJSON(t, map[string]any{"baseSHA256": base, "reviewIssueReplacements": []any{f}})
			emitQuestionHandoffFixture(t, w, protocol, repairs, "", "submit_stage_repair", string(patch))
			return
		}
		output := workflowAISchemaFixture(schema).(map[string]any)
		properties := schema["properties"].(map[string]any)
		switch {
		case properties["qualityMethod"] != nil:
			methods++
			output["qualityMethod"] = "Keep the classroom design unvalidated; no empirical claims."
		case properties["status"] != nil:
			designs++
			text := "Actual original classroom reading draft. Unvalidated."
			if materialDefect && designs == 1 {
				text = "Material is not drafted yet."
			}
			if characterCopy {
				text = "Actual original classroom reading draft. Unvalidated. 誉写"
			}
			if input["_userRevision"] != nil {
				text = "Actual revised original classroom reading draft. Still unvalidated."
			}
			output["status"] = "research_design_not_empirical_result"
			output["deliverables"] = []any{map[string]any{"title": "Reading material", "markdown": text}}
			if probe != nil && probe.material && input["_userRevision"] != nil {
				probe.mu.Lock()
				if !probe.isolationRejected {
					// A valid schema and a correctly selected object are not enough:
					// the model also changed its unselected title in the same draft.
					output["deliverables"].([]any)[0].(map[string]any)["title"] = "Unauthorized title"
					probe.isolationRejected = true
				} else {
					if !strings.Contains(string(body), "/deliverables/0/title") {
						t.Error("model correction did not receive the exact unauthorized path")
					}
					probe.isolationAccepted = true
				}
				probe.mu.Unlock()
			}
			if designs > 1 && input["_userRevision"] == nil {
				corrections, ok := input["_reviewCorrections"].(map[string]any)
				if !ok || corrections["writeContract"] == nil || !strings.Contains(string(mustBootstrapJSON(t, corrections)), "editTargets") {
					t.Error("repair lost original write-object binding")
				}
			}
		case properties["markdown"] != nil:
			reports++
			if textEdit {
				output["methodSummary"] = strings.Repeat("Original full method description. ", 30)
			}
			output["markdown"] = "# Classroom proposal\nA provisional narrative; no collected data."
			if reports > 1 {
				output["markdown"] = "# Classroom proposal\nThe narrative now states the precise, still unvalidated design boundary."
			}
			if input["_userRevision"] != nil {
				output["markdown"] = "# Classroom proposal\nThe requested introduction typo is corrected. Still an unvalidated proposal."
			}
		case properties["reviewedInputSha256"] != nil:
			reviews++
			context, ok := input["context"].(map[string]any)
			parts, _ := input["reportRecordAppendices"].([]any)
			if !ok || len(parts) != 1 || strings.Contains(fmt.Sprint(context["markdown"]), "Material is not drafted") {
				t.Error("design materials not separated from authored narrative")
			}
			contract, ok := input["revisionWriteContract"].(map[string]any)
			if !ok {
				t.Error("missing ownership contract")
				return
			}
			output["reviewedInputSha256"] = workflowAITestStageInputSHA256(prompt)
			output["approved"] = reports > 1
			if characterCopy && reports > 1 {
				if !strings.Contains(string(mustBootstrapJSON(t, input["designContext"])), "Unvalidated. 誊写") {
					t.Error("character selection failed before independent scientific review")
					output["approved"] = false
				}
			}
			applyResearchAcceptanceFixture(output, prompt)
			if reports == 1 {
				issue := "Clarify only the authored narrative; the upstream material is correct."
				if materialDefect {
					issue = "Replace the unfinished reading material at its source."
				}
				if characterCopy {
					issue = "仅将材料正文中的誉写改为誊写，不改其他字段。"
				}
				output["methodIssues"] = []string{issue}
				output["requiredCorrections"] = []string{issue}
				checks := output["acceptanceChecks"].([]any)
				checks[0].(map[string]any)["status"] = "not_met"
			}
			applyTrackedReviewFixture(output, prompt)
			if reports == 1 {
				kind := "report_authored_body"
				if materialDefect {
					kind = "frozen_design_material"
				}
				id := ""
				for _, v := range contract["objects"].([]any) {
					o := v.(map[string]any)
					if o["kind"] == kind {
						id = o["id"].(string)
						break
					}
				}
				if id == "" {
					t.Error("missing requested object")
					return
				}
				plan := output["revisionPlan"].([]any)[0].(map[string]any)
				// Intentionally wrong for the first material proposal. A native
				// local repair must preserve the negative scientific verdict.
				plan["nodeId"], plan["editTargets"] = "report_drafting", []string{id}
				if materialDefect {
					finding := output["reviewFindings"].([]any)[0].(map[string]any)
					quote := "Material is not drafted yet."
					if characterCopy {
						quote = "誉写"
					}
					finding["basis"] = []any{map[string]any{"pointer": "/designContext/deliverables/0/markdown", "quote": quote}}
				}
			}
			wire, err := workflow.IssueReviewSubmission(mustBootstrapJSON(t, output), frozenReviewSchema)
			if err != nil {
				t.Error(err)
				return
			}
			if reports == 1 && materialDefect {
				_ = json.Unmarshal(wire, &rejectedWire)
			}
			emitQuestionHandoffFixture(t, w, protocol, reviews, "", "submit_stage_result", string(wire))
			return
		}
		if textEdit && input["_revisionEdit"] != nil {
			controlName := "submit_stage_edit"
			if characterCopy && properties["status"] != nil {
				controlName = "submit_stage_character_edit"
			}
			for _, control := range request.Tools {
				if control.Function.Name != controlName {
					continue
				}
				props := control.Function.Parameters["properties"].(map[string]any)
				base := props["baseSHA256"].(map[string]any)["enum"].([]any)[0]
				if controlName == "submit_stage_character_edit" {
					source := props["sourceSHA256"].(map[string]any)["enum"].([]any)[0]
					patch := mustBootstrapJSON(t, map[string]any{"baseSHA256": base, "sourceSHA256": source, "characterEdits": []any{map[string]any{"path": "/deliverables/0/markdown", "before": "誉", "replacementId": "U+8A8A"}}})
					emitQuestionHandoffFixture(t, w, protocol, methods+designs+reports, "", controlName, string(patch))
					return
				}
				edits := []any{}
				if properties["status"] != nil {
					edits = append(edits, map[string]any{"path": "/deliverables/0/markdown", "before": "Material is not drafted yet.", "after": "Actual original classroom reading draft. Unvalidated."})
				}
				patch := mustBootstrapJSON(t, map[string]any{"baseSHA256": base, "textEdits": edits})
				emitQuestionHandoffFixture(t, w, protocol, methods+designs+reports, "", "submit_stage_edit", string(patch))
				return
			}
			t.Error("new frozen revision did not expose its native edit control")
			return
		}
		emitQuestionHandoffFixture(t, w, protocol, methods+designs+reports, "```json\n"+string(mustBootstrapJSON(t, output))+"\n```", "", "")
	}))
	defer server.Close()
	options := Options{RootDir: t.TempDir(), EventPublisher: workflowAITestPublisher{}}
	app, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	app.Startup(context.Background())
	profile := saveWorkflowAITestProfile(t, app, server.URL)
	profile, err = app.ModelFacade.SaveModelProfile(modelprofile.SaveCommand{ID: profile.ID, Name: profile.Name, BaseURL: server.URL + "/v1", APIProtocol: protocol, ModelID: workflowAITestModelID, Models: profile.Models, TimeoutSeconds: 20, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	project, err := app.ProjectFacade.CreateProject(wailstransport.CreateProjectRequest{Name: "Material ownership isolated fixture"})
	if err != nil {
		t.Fatal(err)
	}
	reviewBytes, err := os.ReadFile("testdata/review-kernel/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var reviewSchema map[string]any
	_ = json.Unmarshal(reviewBytes, &reviewSchema)
	planProps := reviewSchema["properties"].(map[string]any)["revisionPlan"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	planProps["editTargets"] = map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}}
	frozenReviewSchema = mustBootstrapJSON(t, reviewSchema)
	var reportSchema json.RawMessage
	for _, n := range referenceWorkflowTemplate(t, app, "trusted-research-closure").Definition.Nodes {
		if n.ID == "draft_report" {
			reportSchema = n.OutputSchema
		}
	}
	if textEdit {
		var schema map[string]any
		_ = json.Unmarshal(reportSchema, &schema)
		schema["properties"].(map[string]any)["methodSummary"] = map[string]any{"type": "string", "minLength": 1}
		reportSchema = mustBootstrapJSON(t, schema)
	}
	definition := workflow.Definition{SchemaVersion: 1, Name: "Frozen materials versus report narrative", Inputs: []workflow.Port{{Name: "contract", Type: workflow.TypeObject, Required: true}}, Nodes: []workflow.Node{
		{ID: "method_selection", Name: "Method", Kind: workflow.NodeAIAnalysis, Arguments: json.RawMessage(`{}`), PromptVersion: "material-fixture-v1", Prompt: "Describe the method.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["qualityMethod"],"properties":{"qualityMethod":{"type":"string"}},"additionalProperties":false}`)},
		{ID: "research_design", Name: "Design material source", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "material-fixture-design-v1", Prompt: "Write the actual unvalidated material.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: json.RawMessage(`{"type":"object","required":["status","deliverables"],"properties":{"status":{"type":"string","const":"research_design_not_empirical_result"},"deliverables":{"type":"array","items":{"type":"object","required":["title","markdown"],"properties":{"title":{"type":"string"},"markdown":{"type":"string"}},"additionalProperties":false}}},"additionalProperties":false}`)},
		{ID: "report_drafting", Name: "Report", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "dynamic-report-v6-dm2", Prompt: "Write only the authored narrative.", ReviewPolicy: workflow.AIReviewAuto, OutputSchema: reportSchema},
		{ID: "independent_review", Name: "Independent review", Kind: workflow.NodeAgentStage, Arguments: json.RawMessage(`{}`), PromptVersion: "material-fixture-review-v1", Prompt: "Independently check all deliverables.", ReviewPolicy: workflow.AIReviewAuto, AllowedTools: []string{workflow.ResearchTaskReadTool}, OutputSchema: mustBootstrapJSON(t, reviewSchema)},
		{ID: "delivery_gate", Name: "Delivery gate", Kind: workflow.NodeTool, ToolName: "builtin.research.workflow.review.gate", Arguments: json.RawMessage(`{}`)},
	}, Outputs: []workflow.Output{{Name: "report_draft", Type: workflow.TypeObject, FromNode: "report_drafting", FromPort: "analysis", Required: true}, {Name: "delivery_gate", Type: workflow.TypeObject, FromNode: "delivery_gate", FromPort: "structured", Required: true}}}
	for _, node := range []string{"method_selection", "research_design", "report_drafting", "independent_review"} {
		definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "$input", FromPort: "contract", ToNode: node, ToPort: "researchContract"})
	}
	definition.Edges = append(definition.Edges, workflow.Edge{FromNode: "method_selection", FromPort: "analysis", ToNode: "research_design", ToPort: "methodContext"}, workflow.Edge{FromNode: "method_selection", FromPort: "analysis", ToNode: "independent_review", ToPort: "methodContext"}, workflow.Edge{FromNode: "research_design", FromPort: "analysis", ToNode: "report_drafting", ToPort: "context"}, workflow.Edge{FromNode: "research_design", FromPort: "analysis", ToNode: "independent_review", ToPort: "designContext"}, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "independent_review", ToPort: "context"}, workflow.Edge{FromNode: "report_drafting", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "subject"}, workflow.Edge{FromNode: "independent_review", FromPort: "analysis", ToNode: "delivery_gate", ToPort: "review"})
	saved, err := app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: project.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	started, err := app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: project.ID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"contract":{"successCriteria":["Provide the actual unvalidated original material and an accurate narrative"]}}`), PermissionMode: "full_access", ModelProfileID: profile.ID, ModelID: workflowAITestModelID})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForWorkflowState(t, app, project.ID, started.Run.ID, workflow.RunFailed, 30*time.Second)
	rec := failed.ReviewRevisionRecommendation
	want := "report_drafting"
	if materialDefect {
		want = "research_design"
	}
	if failed.Run.ErrorCode != "RESEARCH_REVIEW_NOT_APPROVED" || rec == nil || rec.Status != "recommended" || rec.NodeID != want {
		t.Fatal("did not reach the negative scientific verdict", failed.Run.ErrorCode, failed.Run.ErrorMessage, rec)
	}
	priorMethod, priorDesign := failed.Steps[0], failed.Steps[1]
	priorExecutions := append([]workflow.AIExecution(nil), failed.AIExecutions...)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	restored, err := app.WorkflowFacade.GetRun(project.ID, failed.Run.ID)
	if err != nil || restored.ReviewRevisionRecommendation == nil || restored.ReviewRevisionRecommendation.ReviewOutputSHA256 != rec.ReviewOutputSHA256 {
		t.Fatal("restart changed review binding", err)
	}
	if _, err := app.WorkflowFacade.Retry(workflow.RetryCommand{ProjectID: project.ID, RunID: failed.Run.ID, StepID: failed.Steps[4].ID, UseRecommendation: true, ExpectedReviewSHA256: rec.ReviewOutputSHA256, ConfirmSideEffect: true}); err != nil {
		t.Fatal(err)
	}
	completed := waitForWorkflowState(t, app, project.ID, failed.Run.ID, workflow.RunCompleted, 30*time.Second)
	if characterCopy {
		var prior, current map[string]any
		_ = json.Unmarshal(failed.Steps[1].Output, &prior)
		_ = json.Unmarshal(completed.Steps[1].Output, &current)
		prior["analysis"].(map[string]any)["deliverables"].([]any)[0].(map[string]any)["markdown"] = "Actual original classroom reading draft. Unvalidated. 誊写"
		if !reflect.DeepEqual(prior["analysis"], current["analysis"]) {
			t.Fatal("character-copy changed more than the selected source rune")
		}
	}
	if textEdit {
		var prior, current map[string]json.RawMessage
		var original, updated struct{ Analysis map[string]json.RawMessage }
		_ = json.Unmarshal(failed.Steps[2].Output, &original)
		_ = json.Unmarshal(completed.Steps[2].Output, &updated)
		prior, current = original.Analysis, updated.Analysis
		for key, value := range prior {
			if key != "markdown" && string(value) != string(current[key]) {
				t.Fatal("explicit report reuse rewrote author metadata", key)
			}
		}
		if len(prior) != len(current) || !strings.Contains(string(current["markdown"]), "# Classroom proposal\\nA provisional narrative; no collected data.") || !strings.Contains(string(current["markdown"]), "Actual original classroom reading draft.") || strings.Contains(string(current["markdown"]), "Material is not drafted yet.") {
			t.Fatal("reuse lost the original body or failed to assemble current design records")
		}
		if characterCopy && (!strings.Contains(string(current["markdown"]), "誊写") || strings.Contains(string(current["markdown"]), "誉写")) {
			t.Fatal("report did not deterministically assemble the corrected material")
		}
	}
	if !reflect.DeepEqual(priorMethod, completed.Steps[0]) {
		t.Fatal("unrelated method reran")
	}
	if !materialDefect && !reflect.DeepEqual(priorDesign, completed.Steps[1]) {
		t.Fatal("report-only repair changed correct design")
	}
	for _, prior := range priorExecutions {
		found := false
		for _, current := range completed.AIExecutions {
			if current.ID == prior.ID {
				found = true
				if !reflect.DeepEqual(prior, current) {
					t.Fatal("old execution mutated")
				}
			}
		}
		if !found {
			t.Fatal("old execution lost")
		}
	}
	var receipt json.RawMessage
	for _, event := range completed.Events {
		if event.Type == "workflow.review_revision_queued" {
			receipt = event.Payload
		}
	}
	var savedReceipt struct {
		WriteContract json.RawMessage `json:"writeContract"`
		Hash          string          `json:"writeContractSha256"`
		Isolation     struct {
			Version, SnapshotSHA256 string
			EditTargets             []string
		} `json:"designWriteIsolation"`
	}
	if json.Unmarshal(receipt, &savedReceipt) != nil || len(savedReceipt.WriteContract) == 0 || fmt.Sprintf("%x", sha256.Sum256(savedReceipt.WriteContract)) != savedReceipt.Hash {
		t.Fatal("missing durable write receipt")
	}
	if savedReceipt.Isolation.Version != "design-material-isolation-v1" || savedReceipt.Isolation.SnapshotSHA256 == "" || len(savedReceipt.Isolation.EditTargets) == 0 {
		t.Fatal("missing actual review write isolation")
	}
	var frozenCount int
	if err := app.store.DB().QueryRow(`SELECT count(*) FROM workflow_revision_write_snapshots WHERE workflow_run_id=?`, completed.Run.ID).Scan(&frozenCount); err != nil || frozenCount != 1 {
		t.Fatal("review baseline was not privately frozen", frozenCount, err)
	}
	if strings.Contains(string(receipt), `"steps":`) || strings.Contains(string(receipt), `"compilation":`) {
		t.Fatal("full private snapshot leaked into event/timeline")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	app.Startup(context.Background())
	after, err := app.WorkflowFacade.GetRun(project.ID, completed.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReceipt := false
	for _, event := range after.Events {
		if event.Type == "workflow.review_revision_queued" {
			foundReceipt = true
			if string(event.Payload) != string(receipt) {
				t.Fatal("SQLite reopen changed receipt bytes")
			}
		}
	}
	if !foundReceipt {
		t.Fatal("SQLite reopen lost receipt")
	}
	mu.Lock()
	counts := []int{methods, designs, reports, reviews, repairs}
	mu.Unlock()
	wantDesigns, wantRepairs := 1, 0
	if materialDefect {
		wantDesigns, wantRepairs = 2, 1
	}
	if !reflect.DeepEqual(counts, []int{1, wantDesigns, 2, 2, wantRepairs}) {
		t.Fatal("unexpected model work", counts)
	}
	if materialDefect && !strings.Contains(string(completed.Steps[1].Output), "Actual original") {
		t.Fatal("material source was not really repaired")
	}
	if completed.DeliveryAssessment == nil || completed.DeliveryAssessment.Status != "reviewed" {
		t.Fatal("original gate not verified", completed.DeliveryAssessment)
	}
	if probe != nil {
		probe.exercise(t, &app, options, profile.ID, completed)
	}
}
