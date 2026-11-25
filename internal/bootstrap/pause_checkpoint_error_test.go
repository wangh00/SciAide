package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/modelcap"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestPauseRunningReportsCheckpointBoundaryWithoutMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		emitQuestionHandoffFixture(t, w, modelprofile.ProtocolOpenAIResponses, 1, "Fixture discussion only.", "", "")
	}))
	defer server.Close()
	f := newDebugFixture(t, server.URL, modelprofile.ProtocolOpenAIResponses, 2, false)
	rid, _ := id.New()
	c, _ := f.create(t, rid, false)
	f.wait(t, c, func(v map[string]json.RawMessage) bool { return debugState(v) == "discussion_idle" })
	// A direct task owns no starter Run, so establish just the guard's
	// precondition with an owned running Run in this temporary database; no
	// active model or user task is interrupted by this fixture.
	now := time.Now().UTC()
	workflowID, _ := id.New()
	versionID, _ := id.New()
	at := now.Format(time.RFC3339Nano)
	if _, err := f.app.store.DB().Exec(`INSERT INTO workflows(id,project_id,name,purpose,version,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`, workflowID, c.ProjectID, "Pause guard", "research_starter", 1, at, at); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if _, err := f.app.store.DB().Exec(`INSERT INTO workflow_versions(id,workflow_id,version_number,definition_json,definition_sha256,compilation_json,compilation_sha256,created_at) VALUES (?,?,1,'{}',?,'{}',?,?)`, versionID, workflowID, hash, hash, at); err != nil {
		t.Fatal(err)
	}
	inputDigest := sha256.Sum256([]byte("{}"))
	inputsHash := hex.EncodeToString(inputDigest[:])
	compilationDigest := sha256.Sum256([]byte("{}"))
	compilationHash := hex.EncodeToString(compilationDigest[:])
	runID, _ := id.New()
	conversationID, _ := id.New()
	guardRun := workflow.Run{ID: runID, ProjectID: c.ProjectID, ResearchTaskID: c.TaskID, ConversationID: conversationID, WorkflowID: workflowID, WorkflowVersionID: versionID, WorkflowPurpose: workflow.PurposeResearchStarter, Status: workflow.RunRunning, PermissionMode: conversation.PermissionFullAccess, Inputs: json.RawMessage(`{}`), InputsSHA256: inputsHash, Outputs: json.RawMessage(`{}`), Compilation: workflow.Compilation{}, CompilationSHA256: compilationHash, CreatedAt: now, UpdatedAt: now}
	guardConversation := conversation.Conversation{ID: conversationID, ProjectID: c.ProjectID, Title: "Pause guard fixture", ModelProfileID: c.ModelProfileID, ModelID: c.ModelID, PermissionMode: conversation.PermissionFullAccess, ReasoningLevel: modelcap.DefaultReasoningLevel, CreatedAt: now, UpdatedAt: now}
	eventID, _ := id.New()
	if err := sqlite.NewWorkflowRuntimeRepository(f.app.store.DB()).CreateResearchRun(context.Background(), guardRun, nil, workflow.RuntimeEvent{ID: eventID, WorkflowRunID: runID, Type: "workflow.created", Payload: json.RawMessage(`{}`), CreatedAt: now}, guardConversation); err != nil {
		t.Fatal(err)
	}
	code, result := f.request(t, "POST", "/v1/cases/"+c.ID+"/actions/pause", map[string]any{"runId": runID})
	var failure struct {
		Code      string
		Retryable bool
	}
	if err := json.Unmarshal(result["error"], &failure); err != nil || code != http.StatusBadRequest || failure.Code != "WORKFLOW_PAUSE_REQUIRES_CHECKPOINT" || failure.Retryable {
		t.Fatal("checkpoint restriction returned a generic/retryable failure", code, string(result["error"]))
	}
	var status string
	if err := f.app.store.DB().QueryRow(`SELECT status FROM workflow_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "running" {
		t.Fatal("pause cancelled or rewound current execution", status, err)
	}
}
