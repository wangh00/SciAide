package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/research"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
)

type requestBoundaryMaterialFixture struct{ changed atomic.Bool }

func (f *requestBoundaryMaterialFixture) GetCandidateForTask(context.Context, string, string, string) (research.Candidate, error) {
	attachment := "old"
	if f.changed.Load() {
		attachment = "new"
	}
	return research.Candidate{ImportStatus: research.ImportImported, ImportKind: research.ImportFullText, AttachmentID: attachment}, nil
}

// A real application/HTTP adapter/SQLite fixture proves that a tool boundary
// cannot send another obsolete model request. The deliberately untrusted route
// has no import/sync/search chain: it must STOP rather than guess a refresh.
func TestEvidenceChangeStopsBeforeNextHTTPAndRejectsUnsafeRefresh(t *testing.T) {
	materials := &requestBoundaryMaterialFixture{}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if n == 1 {
			emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, n, "Fixture discussion only.", "", "")
			return
		}
		materials.changed.Store(true) // Persistent material identity changes during this tool turn.
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, n, "", "workspace_list", `{"path":".","limit":1}`)
	}))
	defer server.Close()
	f := newDebugFixture(t, server.URL, modelprofile.ProtocolOpenAIResponses, 4, true)
	f.app.workflow.SetEvidenceMaterialReader(materials)
	rid, _ := id.New()
	c, _ := f.create(t, rid, true)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
	saved, err := f.app.WorkflowFacade.Save(workflow.SaveCommand{ProjectID: c.ProjectID, Definition: workflow.Definition{SchemaVersion: 1, Name: "request boundary fixture", Inputs: []workflow.Port{{Name: "materials", Type: workflow.TypeArray, Required: true}, {Name: "documents", Type: workflow.TypeArray, Required: true}}, Nodes: []workflow.Node{{ID: "evidence_screening", Name: "evidence boundary", Kind: workflow.NodeAgentStage, ReviewPolicy: workflow.AIReviewAuto, Arguments: json.RawMessage(`{}`), PromptVersion: "selected-evidence-v6", Prompt: "Read selected evidence.", AllowedTools: []string{"builtin.workspace.list"}, OutputSchema: json.RawMessage(`{"type":"object","required":["summary","recommendedReferences","citationAssessments","recommendation","coverage"],"properties":{"summary":{"type":"string"},"recommendedReferences":{"type":"array","items":{"type":"string"}},"citationAssessments":{"type":"array","items":{"type":"object"}},"recommendation":{"type":"string"},"coverage":{"type":"object"},"documentAnalyses":{"type":"array","items":{"type":"object"}}}}`)}}, Edges: []workflow.Edge{{FromNode: "$input", FromPort: "materials", ToNode: "evidence_screening", ToPort: "importedMaterials"}, {FromNode: "$input", FromPort: "documents", ToNode: "evidence_screening", ToPort: "documentIds"}}, Outputs: []workflow.Output{{Name: "result", Type: workflow.TypeObject, FromNode: "evidence_screening", FromPort: "analysis", Required: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := f.app.WorkflowFacade.Start(workflow.StartCommand{ProjectID: c.ProjectID, ResearchTaskID: workflow.NewResearchTaskID, WorkflowID: saved.Workflow.ID, Inputs: json.RawMessage(`{"materials":[{"candidateId":"c","attachmentId":"old"}],"documents":["fixture-document"]}`), ModelProfileID: f.profileID, ModelID: f.modelID, PermissionMode: conversation.PermissionFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForWorkflowState(t, f.app, c.ProjectID, started.Run.ID, workflow.RunFailed, 10*time.Second)
	if calls.Load() != 2 || done.Run.ErrorCode != "WORKFLOW_EVIDENCE_REFRESH_FAILED" || len(done.AIExecutions) != 1 {
		t.Fatal("obsolete request sent or unsafe refresh accepted", calls.Load(), done.Run.ErrorCode, len(done.AIExecutions))
	}
	chat, err := f.app.ChatFacade.GetRunSnapshot(done.AIExecutions[0].ChatRunID)
	if err != nil || chat.Run.ErrorCode != "WORKFLOW_EVIDENCE_UPDATED" || chat.Run.ModelTurns != 1 {
		t.Fatal("request-boundary handoff was lost", err, chat.Run.ErrorCode, chat.Run.ModelTurns)
	}
	var input struct {
		Materials []struct {
			AttachmentID string `json:"attachmentId"`
		} `json:"importedMaterials"`
	}
	if err := json.Unmarshal(done.Steps[0].Input, &input); err != nil || len(input.Materials) != 1 || input.Materials[0].AttachmentID != "old" {
		t.Fatal("old frozen input was silently rewritten", err)
	}
}
