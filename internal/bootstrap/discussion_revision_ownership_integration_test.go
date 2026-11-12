package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangh00/SciAide/internal/app/chat"
	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/workflow"
	"github.com/wangh00/SciAide/internal/id"
	"github.com/wangh00/SciAide/internal/storage/sqlite"
)

func TestDiscussionMaterialOwnershipAcrossProtocols(t *testing.T) {
	for _, protocol := range []modelprofile.APIProtocol{modelprofile.ProtocolOpenAIChat, modelprofile.ProtocolOpenAIResponses, modelprofile.ProtocolAnthropic} {
		for _, material := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/material_%t", protocol, material), func(t *testing.T) {
				testMaterialOwnershipRevision(t, protocol, material, true)
			})
		}
	}
}

// The fixture reads the actual paged overview through each real adapter. It
// never invents an edit ID or accesses a production task/model/SQLite root.
type discussionOwnershipProbe struct {
	mu                         sync.Mutex
	protocol                   modelprofile.APIProtocol
	material                   bool
	mode                       string
	requested, sent            bool
	next, calls, revisedInputs int
	pageHash                   string
	content                    strings.Builder
	confirmed                  workflow.ResearchRevisionProposal
	isolationRejected          bool
	isolationAccepted          bool
}

type discussionOverviewPage struct {
	Section, Content, SHA256 string
	Offset, NextOffset       int
	HasMore                  bool
}

func discussionPageInWire(data []byte, offset int) *discussionOverviewPage {
	var root any
	if json.Unmarshal(data, &root) != nil {
		return nil
	}
	var result *discussionOverviewPage
	var visit func(any, int)
	visit = func(v any, depth int) {
		if depth > 12 {
			return
		}
		switch value := v.(type) {
		case map[string]any:
			if value["section"] == "overview" && value["content"] != nil && value["sha256"] != nil {
				encoded, _ := json.Marshal(value)
				var page discussionOverviewPage
				if json.Unmarshal(encoded, &page) == nil && page.Offset == offset {
					result = &page
				}
			}
			for _, child := range value {
				visit(child, depth+1)
			}
		case []any:
			for _, child := range value {
				visit(child, depth+1)
			}
		case string:
			text := strings.TrimSpace(value)
			text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "<untrusted_tool_result>"), "</untrusted_tool_result>"))
			if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
				var decoded any
				if json.Unmarshal([]byte(text), &decoded) == nil {
					visit(decoded, depth+1)
				}
			}
		}
	}
	visit(root, 0)
	return result
}

func (p *discussionOwnershipProbe) respond(t *testing.T, w http.ResponseWriter, wire, canonical []byte) bool {
	var request struct {
		Tools []struct{ Function struct{ Name string } }
	}
	_ = json.Unmarshal(canonical, &request)
	discussion := false
	for _, candidate := range request.Tools {
		discussion = discussion || candidate.Function.Name == "research_revision_propose"
	}
	if !discussion {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.sent {
		emitQuestionHandoffFixture(t, w, p.protocol, p.calls, "The proposal result is recorded. No stages have been reopened; only explicit user confirmation can do so.", "", "")
		return true
	}
	if p.requested {
		page := discussionPageInWire(wire, p.next)
		if page == nil || page.NextOffset <= page.Offset || p.pageHash != "" && p.pageHash != page.SHA256 {
			t.Error("actual overview page missing, changed, or made no progress", p.next)
			var diagnostic map[string]json.RawMessage
			_ = json.Unmarshal(wire, &diagnostic)
			for _, key := range []string{"messages", "input"} {
				var messages []json.RawMessage
				_ = json.Unmarshal(diagnostic[key], &messages)
				for _, message := range messages {
					text := string(message)
					if strings.Contains(text, `"role":"tool"`) || strings.Contains(text, `"type":"tool_result"`) || strings.Contains(text, `"type":"function_call_output"`) {
						t.Log("fixture result wire", text[:min(len(text), 6500)])
					}
				}
			}
			http.Error(w, "fixture could not read overview", 500)
			return true
		}
		p.pageHash = page.SHA256
		p.content.WriteString(page.Content)
		p.next = page.NextOffset
		if !page.HasMore {
			if fmt.Sprintf("%x", sha256.Sum256([]byte(p.content.String()))) != p.pageHash {
				t.Error("overview pages did not reconstruct exact bytes")
			}
			var overview struct {
				Capabilities map[string]any `json:"revisionTargetCapabilities"`
				Contract     struct {
					Version string
					Objects []struct{ ID, Kind, OwnerNodeID string }
				} `json:"revisionWriteContract"`
			}
			if json.Unmarshal([]byte(p.content.String()), &overview) != nil || overview.Contract.Version != "revision-write-objects-v1" || len(overview.Capabilities) == 0 {
				t.Error("discussion did not receive the real ownership navigation")
			}
			kind := "report_authored_body"
			if p.material || p.mode == "wrong_owner" {
				kind = "frozen_design_material"
			}
			command := workflow.ProposeResearchRevisionCommand{NodeID: "report_drafting", Summary: "Correct requested spelling", Changes: []string{"Correct the report introduction typo; preserve scientific caveats"}, Reason: "Use the actual declared write owners"}
			for _, object := range overview.Contract.Objects {
				if object.Kind == kind {
					command.EditTargets = []string{object.ID}
					if p.material && p.mode == "valid" {
						command.NodeID = object.OwnerNodeID
						command.Changes = []string{"Correct the frozen reading material and report introduction typos without changing the science"}
					}
					break
				}
			}
			if len(command.EditTargets) != 1 {
				t.Error("actual editable object missing", kind)
			}
			if p.mode == "missing" {
				command.EditTargets = nil
			} else if p.mode == "valid" && p.material {
				for _, object := range overview.Contract.Objects {
					if object.Kind == "report_authored_body" {
						command.EditTargets = append(command.EditTargets, object.ID)
					}
				}
			}
			p.sent = true
			emitQuestionHandoffFixture(t, w, p.protocol, p.calls, "", "research_revision_propose", string(mustBootstrapJSON(t, command)))
			return true
		}
	}
	p.requested = true
	emitQuestionHandoffFixture(t, w, p.protocol, p.calls, "", "research_task_read", fmt.Sprintf(`{"section":"overview","offset":%d}`, p.next))
	return true
}

func (p *discussionOwnershipProbe) checkStage(t *testing.T, input map[string]any) {
	v, ok := input["_userRevision"].(map[string]any)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revisedInputs++
	var expected any
	_ = json.Unmarshal(p.confirmed.WriteContract, &expected)
	if v["designWriteIsolationVersion"] != p.confirmed.DesignWriteIsolationVersion && p.material || v["writeContractVersion"] != p.confirmed.WriteContractVersion || v["writeContractSha256"] != p.confirmed.WriteContractSHA256 || !reflect.DeepEqual(v["writeContract"], expected) || string(mustBootstrapJSON(t, v["editTargets"])) != string(mustBootstrapJSON(t, p.confirmed.EditTargets)) || string(mustBootstrapJSON(t, v["changes"])) != string(mustBootstrapJSON(t, p.confirmed.Changes)) {
		t.Error("a downstream execution lost the original confirmed write declaration")
	}
}

func (p *discussionOwnershipProbe) exercise(t *testing.T, appPtr **Application, options Options, profileID string, original workflow.RunDetail) {
	t.Helper()
	app := *appPtr
	baseline := workflow.ResearchRevisionSnapshot(original)
	for _, mode := range []string{"missing", "wrong_owner", "valid"} {
		p.mu.Lock()
		p.mode, p.requested, p.sent, p.next, p.pageHash = mode, false, false, 0, ""
		p.content.Reset()
		p.mu.Unlock()
		messageID, _ := id.New()
		run, err := app.ChatFacade.StartChat(chat.StartCommand{ConversationID: original.Run.ConversationID, ModelProfileID: profileID, ModelID: workflowAITestModelID, Text: "User requests typo correction only; propose in the same task and preserve all original materials and checks.", ClientMessageID: messageID})
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			snapshot, err := app.ChatFacade.GetRunSnapshot(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Run.Status == chat.RunCompleted {
				found := false
				for _, call := range snapshot.ToolCalls {
					if call.ToolName != workflow.ResearchRevisionProposeTool {
						continue
					}
					found = true
					if (call.Status == "completed") != (mode == "valid") {
						t.Fatalf("proposal unexpectedly accepted/rejected: %s %+v", mode, call)
					}
				}
				if !found {
					t.Fatal("discussion did not invoke the actual proposal tool")
				}
				break
			}
			if snapshot.Run.Status == chat.RunFailed || time.Now().After(deadline) {
				t.Fatal("discussion fixture failed", mode, snapshot.Run)
			}
		}
		detail, err := app.WorkflowFacade.GetRun(original.Run.ProjectID, original.Run.ID)
		if err != nil || workflow.ResearchRevisionSnapshot(detail) != baseline {
			t.Fatal("proposal modified completed stages before confirmation", err)
		}
		if mode != "valid" && len(detail.RevisionProposals) != 0 {
			t.Fatal("invalid proposal was persisted", mode)
		}
	}
	detail, err := app.WorkflowFacade.GetRun(original.Run.ProjectID, original.Run.ID)
	if err != nil || len(detail.RevisionProposals) != 1 || !detail.RevisionProposals[0].CanConfirm {
		t.Fatal("valid declared proposal missing", err, detail.RevisionProposals)
	}
	proposal := detail.RevisionProposals[0]
	if p.material && proposal.DesignWriteIsolationVersion != "design-material-isolation-v1" || !p.material && proposal.DesignWriteIsolationVersion != "" {
		t.Fatal("new proposal has wrong design isolation policy", proposal.DesignWriteIsolationVersion)
	}
	p.mu.Lock()
	p.confirmed = proposal
	callsBefore := p.calls
	p.mu.Unlock()
	var originalJSON string
	if err := app.store.DB().QueryRow(`SELECT proposal_json FROM research_revision_proposals WHERE id=?`, proposal.ID).Scan(&originalJSON); err != nil {
		t.Fatal(err)
	}
	// Simulate old/corrupt persisted cards, never alter a real debug task.
	modes := []string{"old_unbound", "changed_bytes"}
	if p.material {
		modes = append(modes, "old_v7_policy")
	}
	for _, mode := range modes {
		broken := proposal
		if mode == "old_unbound" {
			broken.EditTargets, broken.WriteContract, broken.WriteContractSHA256, broken.WriteContractVersion = nil, nil, "", ""
		} else if mode == "old_v7_policy" {
			broken.DesignWriteIsolationVersion = ""
		} else {
			var object map[string]any
			_ = json.Unmarshal(broken.WriteContract, &object)
			broken.WriteContract = mustBootstrapJSON(t, object)
		}
		if _, err := app.store.DB().Exec(`UPDATE research_revision_proposals SET proposal_json=? WHERE id=?`, string(mustBootstrapJSON(t, broken)), proposal.ID); err == nil {
			t.Fatal("proposal immutability trigger is disabled")
		}
		seedLegacyPendingProposalFixture(t, app, proposal.ID, string(mustBootstrapJSON(t, broken)))
		assertBlockedDiscussionProposal(t, app, original, proposal.ID)
		eventID, _ := id.New()
		event := workflow.RuntimeEvent{ID: eventID, WorkflowRunID: original.Run.ID, Type: "workflow.user_revision_queued", Payload: mustBootstrapJSON(t, map[string]any{"proposal": broken}), CreatedAt: time.Now()}
		if err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).ConfirmResearchRevision(context.Background(), broken, detail, event); err == nil {
			t.Fatal("repository confirmation bypassed invalid persisted binding", mode)
		}
		seedLegacyPendingProposalFixture(t, app, proposal.ID, originalJSON)
	}
	// Caller/event substitution must fail even when object IDs are valid.
	changed := proposal
	changed.Changes = []string{"Unexpected replacement of confirmed changes"}
	eventID, _ := id.New()
	payload, err := workflow.ResearchRevisionConfirmationPayload(detail, changed)
	if err != nil {
		t.Fatal(err)
	}
	event := workflow.RuntimeEvent{ID: eventID, WorkflowRunID: original.Run.ID, Type: "workflow.user_revision_queued", Payload: mustBootstrapJSON(t, payload), CreatedAt: time.Now()}
	repo := sqlite.NewWorkflowRuntimeRepository(app.store.DB())
	if repo.ConfirmResearchRevision(context.Background(), changed, detail, event) == nil || repo.ConfirmResearchRevision(context.Background(), proposal, detail, event) == nil {
		t.Fatal("repository accepted substituted proposal/event")
	}
	payload, err = workflow.ResearchRevisionConfirmationPayload(detail, proposal)
	if err != nil {
		t.Fatal(err)
	}
	payload["startOrdinal"] = 999
	event.Payload = mustBootstrapJSON(t, payload)
	if repo.ConfirmResearchRevision(context.Background(), proposal, detail, event) == nil {
		t.Fatal("repository accepted a valid proposal with a substituted replay ordinal")
	}
	p.mu.Lock()
	if p.calls != callsBefore {
		t.Error("a rejected confirmation called the model")
	}
	p.mu.Unlock()
	unchanged, err := app.WorkflowFacade.GetRun(original.Run.ProjectID, original.Run.ID)
	if err != nil || workflow.ResearchRevisionSnapshot(unchanged) != baseline || len(unchanged.Events) != len(original.Events) {
		t.Fatal("rejected confirmation changed task/history", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	*appPtr = app
	app.Startup(context.Background())
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: original.Run.ProjectID, RunID: original.Run.ID, ProposalID: proposal.ID, Confirmed: true}); err != nil {
		t.Fatal("valid proposal could not confirm after restart", err)
	}
	final := waitForWorkflowState(t, app, original.Run.ProjectID, original.Run.ID, workflow.RunCompleted, 30*time.Second)
	if !reflect.DeepEqual(final.Steps[0], original.Steps[0]) || !p.material && !reflect.DeepEqual(final.Steps[1], original.Steps[1]) {
		t.Fatal("revision reran a reusable upstream stage")
	}
	if p.material && !strings.Contains(string(final.Steps[1].Output), "Actual revised original") {
		t.Fatal("material producer was not really revised")
	}
	if p.material {
		p.mu.Lock()
		rejected, accepted := p.isolationRejected, p.isolationAccepted
		p.mu.Unlock()
		if !rejected || !accepted || strings.Contains(string(final.Steps[1].Output), "Unauthorized title") {
			t.Fatal("actual write isolation did not reject then correct the unselected title", rejected, accepted)
		}
	}
	p.mu.Lock()
	revisedInputs := p.revisedInputs
	p.mu.Unlock()
	if final.DeliveryAssessment == nil || final.DeliveryAssessment.Status != "reviewed" || revisedInputs < 2 || string(final.Run.Inputs) != string(original.Run.Inputs) {
		t.Fatal("revision skipped independent review/original inputs", final.DeliveryAssessment)
	}
	for _, prior := range original.AIExecutions {
		found := false
		for _, current := range final.AIExecutions {
			if current.ID == prior.ID {
				found = true
				if !reflect.DeepEqual(prior, current) {
					t.Fatal("old AI execution was rewritten")
				}
			}
		}
		if !found {
			t.Fatal("old execution was lost")
		}
	}
}

// Seed an older persisted row in this t.TempDir only, without disabling any
// immutability trigger. Real application code cannot update proposal_json.
func seedLegacyPendingProposalFixture(t *testing.T, app *Application, proposalID, encoded string) {
	t.Helper()
	tx, err := app.store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var runID, chatID, messageID, callID, created string
	if err := tx.QueryRow(`SELECT workflow_run_id,chat_run_id,user_message_id,source_call_id,created_at FROM research_revision_proposals WHERE id=? AND status='pending'`, proposalID).Scan(&runID, &chatID, &messageID, &callID, &created); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM research_timeline WHERE kind='proposal' AND source_id=?`, proposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM research_revision_proposals WHERE id=? AND status='pending'`, proposalID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO research_revision_proposals(id,workflow_run_id,chat_run_id,user_message_id,source_call_id,status,proposal_json,created_at) VALUES(?,?,?,?,?,'pending',?,?)`, proposalID, runID, chatID, messageID, callID, encoded, created); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertBlockedDiscussionProposal(t *testing.T, app *Application, original workflow.RunDetail, proposalID string) {
	t.Helper()
	detail, err := app.WorkflowFacade.GetRun(original.Run.ProjectID, original.Run.ID)
	if err != nil || len(detail.RevisionProposals) != 1 {
		t.Fatal("cannot read blocked card", err)
	}
	check := func(values []workflow.ResearchRevisionProposal) {
		if len(values) != 1 || values[0].ID != proposalID || values[0].CanConfirm || values[0].Status != "pending" || values[0].ConfirmationBlockedReason == "" {
			t.Fatal("read boundary exposed a confirmable/unexplained old card", values)
		}
	}
	check(detail.RevisionProposals)
	listed, err := sqlite.NewWorkflowRuntimeRepository(app.store.DB()).ListResearchRevisionProposals(context.Background(), original.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(listed)
	page, err := app.WorkflowFacade.ResearchTimeline(workflow.ResearchTimelineQuery{ProjectID: original.Run.ProjectID, TaskID: original.Run.ResearchTaskID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range page.Entries {
		if entry.Proposal != nil && entry.Proposal.ID == proposalID {
			check([]workflow.ResearchRevisionProposal{*entry.Proposal})
			found = true
		}
	}
	if !found {
		t.Fatal("timeline omitted fixture proposal")
	}
	if _, err := app.WorkflowFacade.ConfirmResearchRevision(workflow.ConfirmResearchRevisionCommand{ProjectID: original.Run.ProjectID, RunID: original.Run.ID, ProposalID: proposalID, Confirmed: true}); err == nil {
		t.Fatal("facade confirmed old/corrupt card")
	}
}

// Existing precision/data/source fixtures now act as v7 clients: the frozen
// current review supplies the real body ID. Navigation itself is exercised
// through actual paged task.read HTTP calls by the three-protocol test above.
func reportBodyWriteTargetsFromFrozenReview(t *testing.T, detail workflow.RunDetail) []string {
	t.Helper()
	var ids []string
	for _, step := range detail.Steps {
		var input struct {
			Contract *struct {
				Objects []struct{ ID, Kind, OwnerNodeID string }
			} `json:"revisionWriteContract"`
		}
		if json.Unmarshal(step.Input, &input) != nil || input.Contract == nil {
			continue
		}
		for _, object := range input.Contract.Objects {
			if object.Kind == "report_authored_body" && object.OwnerNodeID == "report_drafting" {
				ids = append(ids, object.ID)
			}
		}
	}
	if len(ids) != 1 {
		t.Fatal("fixture has no unique current report body write identity", ids)
	}
	return ids
}
